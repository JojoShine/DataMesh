package workflow

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Noto Sans SC is distributed under the adjacent SIL Open Font License.
// Rasterizing the mark embeds its actual glyphs without depending on viewer fonts.
//
//go:embed security_pdf_font/NotoSansSC.ttf
var securityPDFFontBytes []byte

var securityPDFFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(securityPDFFontBytes) })
var securityPDFToken = regexp.MustCompile(`^[A-Za-z0-9._-]{8,256}$`)
var securityPDFTrace = regexp.MustCompile(`/DataMeshTrace\s*<<\s*/ID\s*\(([A-Za-z0-9._-]{8,256})\)\s*>>\s*BDC\s*EMC`)
var securityPDFMu sync.Mutex

const securityPDFMaxBytes = 32 << 20
const securityPDFMaxPages = 200

func init() { api.DisableConfigDir() }

func securityPDFConfig() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationStrict
	conf.Offline = true
	conf.Optimize = false
	conf.OptimizeBeforeWriting = false
	conf.WriteObjectStream = false
	conf.Limits = model.ResourceLimits{MaxStreamBytes: 32 << 20, MaxDecodeBytes: 32 << 20, MaxImagePixels: 16_000_000, MaxImageBytes: 64 << 20, MaxObjectCount: 100_000, MaxObjectStreamCount: 10_000, MaxObjectStreamFirst: 4 << 20, MaxXRefEntries: 100_000, MaxRecursionDepth: 64}
	return conf
}

func securityPDFRead(body []byte) (*model.Context, error) {
	if len(body) == 0 || len(body) > securityPDFMaxBytes {
		return nil, problem(413, "SECURITY_FILE_LIMIT", "PDF文件超过32MiB上限")
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		return nil, invalid
	}
	ctx, err := api.ReadContext(bytes.NewReader(body), securityPDFConfig())
	if err != nil {
		return nil, problem(409, "SECURITY_PDF_UNSUPPORTED", "PDF无法安全解析或已加密")
	}
	if ctx.Encrypt != nil {
		return nil, problem(409, "SECURITY_PDF_ENCRYPTED", "加密PDF不能生成受控副本")
	}
	if err = api.ValidateContext(ctx); err != nil {
		return nil, problem(409, "SECURITY_PDF_UNSUPPORTED", "PDF结构不受支持或已损坏")
	}
	if ctx.SignatureExist || len(ctx.Signatures) != 0 {
		return nil, problem(409, "SECURITY_SIGNED_FILE_UNSUPPORTED", "带数字签名的PDF不能重新保存为水印副本")
	}
	// Inspect parsed objects as well as catalog state: escaped PDF names and
	// signatures inside compressed object streams must not bypass this check.
	for _, entry := range ctx.Table {
		if entry.Free {
			continue
		}
		if securityPDFHasSignature(entry.Object, 0) {
			return nil, problem(409, "SECURITY_SIGNED_FILE_UNSUPPORTED", "带数字签名或签名字段的PDF不能重新保存为水印副本")
		}
	}
	if ctx.PageCount < 1 || ctx.PageCount > securityPDFMaxPages {
		return nil, problem(413, "SECURITY_FILE_LIMIT", "PDF最多支持200页")
	}
	remaining := 64 << 20
	for page := 1; page <= ctx.PageCount; page++ {
		d, _, _, err := ctx.PageDict(page, false)
		if err != nil {
			return nil, err
		}
		if _, err = securityPDFPageContent(ctx, d, &remaining); err != nil {
			return nil, err
		}
	}
	return ctx, nil
}

func securityPDFHasSignature(obj types.Object, depth int) bool {
	if depth > 64 {
		return true
	}
	switch obj := obj.(type) {
	case types.StreamDict:
		return securityPDFHasSignature(obj.Dict, depth+1)
	case types.Dict:
		typ, field := obj.NameEntry("Type"), obj.NameEntry("FT")
		if typ != nil && *typ == "Sig" || field != nil && *field == "Sig" || obj["ByteRange"] != nil {
			return true
		}
		for _, value := range obj {
			if securityPDFHasSignature(value, depth+1) {
				return true
			}
		}
	case types.Array:
		for _, value := range obj {
			if securityPDFHasSignature(value, depth+1) {
				return true
			}
		}
	}
	return false
}

// Account for decoded bytes across streams and pages, rather than allowing
// each compressed stream to consume the entire document's decode budget.
func securityPDFPageContent(ctx *model.Context, d types.Dict, remaining *int) ([]byte, error) {
	obj, err := ctx.Dereference(d["Contents"])
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, nil
	}
	streams := types.Array{obj}
	if arr, ok := obj.(types.Array); ok {
		streams = arr
	}
	var content bytes.Buffer
	for _, obj := range streams {
		sd, _, err := ctx.DereferenceStreamDict(obj)
		if err != nil {
			return nil, err
		}
		if sd == nil {
			continue
		}
		if *remaining <= 0 {
			return nil, problem(413, "SECURITY_FILE_LIMIT", "PDF解码内容超过64MiB上限")
		}
		limit := min(*remaining, 32<<20)
		if err = sd.DecodeWithLimit(int64(limit)); err != nil {
			return nil, problem(409, "SECURITY_PDF_UNSUPPORTED", "PDF内容流不受支持或超过解码上限")
		}
		if len(sd.Content) > *remaining {
			return nil, problem(413, "SECURITY_FILE_LIMIT", "PDF解码内容超过64MiB上限")
		}
		*remaining -= len(sd.Content)
		content.Write(sd.Content)
		content.WriteByte('\n')
	}
	return content.Bytes(), nil
}

// A single transformation runs at a time to bound concurrent PDF/font memory.
func securityMarkPDF(body []byte, text, id string) ([]byte, error) {
	securityPDFMu.Lock()
	defer securityPDFMu.Unlock()
	if !securityPDFToken.MatchString(id) || strings.TrimSpace(text) == "" {
		return nil, invalid
	}
	ctx, err := securityPDFRead(body)
	if err != nil {
		return nil, err
	}
	mark, err := securityPDFRenderMark(text + "\n" + id)
	if err != nil {
		return nil, err
	}
	wm, err := api.ImageWatermarkForReader(bytes.NewReader(mark), "position:c, scale:0.85 rel, rotation:30, opacity:0.32", true, false, types.POINTS)
	if err != nil {
		return nil, err
	}
	pages := types.IntSet{}
	for page := 1; page <= ctx.PageCount; page++ {
		pages[page] = true
	}
	if err = api.WatermarkContext(ctx, pages, wm); err != nil {
		return nil, problem(409, "SECURITY_PDF_UNSUPPORTED", "PDF无法安全叠加内容水印")
	}
	return securityPDFWrite(ctx)
}

// securityTracePDF places a recoverable token in every page's content stream.
// The caller authenticates the token and binds the final bytes to its signed
// delivery proof. This trace can be removed by editing; it is not DRM.
func securityTracePDF(body []byte, marker string) ([]byte, error) {
	securityPDFMu.Lock()
	defer securityPDFMu.Unlock()
	if !securityPDFToken.MatchString(marker) {
		return nil, invalid
	}
	ctx, err := securityPDFRead(body)
	if err != nil {
		return nil, err
	}
	for page := 1; page <= ctx.PageCount; page++ {
		d, _, _, err := ctx.PageDict(page, false)
		if err != nil {
			return nil, err
		}
		ref, err := ctx.StreamDictIndRef([]byte("\n/DataMeshTrace << /ID (" + marker + ") >> BDC EMC\n"))
		if err != nil {
			return nil, err
		}
		old := d["Contents"]
		if old == nil {
			d["Contents"] = *ref
			continue
		}
		obj, err := ctx.Dereference(old)
		if err != nil {
			return nil, err
		}
		if arr, ok := obj.(types.Array); ok {
			d["Contents"] = append(append(types.Array{}, arr...), *ref)
		} else {
			d["Contents"] = types.Array{old, *ref}
		}
	}
	out, err := securityPDFWrite(ctx)
	if err != nil {
		return nil, err
	}
	// Verify the persisted representation before it is bound to delivery evidence.
	got, err := securityExtractPDFMarkerLocked(out)
	if err != nil {
		return nil, err
	}
	if got != marker {
		return nil, problem(409, "SECURITY_TRACE_INVALID", "PDF交付标记写入校验失败")
	}
	return out, nil
}

func securityExtractPDFMarker(body []byte) (string, error) {
	securityPDFMu.Lock()
	defer securityPDFMu.Unlock()
	return securityExtractPDFMarkerLocked(body)
}
func securityExtractPDFMarkerLocked(body []byte) (string, error) {
	ctx, err := securityPDFRead(body)
	if err != nil {
		return "", err
	}
	marker := ""
	remaining := 64 << 20
	for page := 1; page <= ctx.PageCount; page++ {
		d, _, _, err := ctx.PageDict(page, false)
		if err != nil {
			return "", err
		}
		content, err := securityPDFPageContent(ctx, d, &remaining)
		if err != nil {
			return "", err
		}
		matches := securityPDFTrace.FindAllSubmatch(content, -1)
		if len(matches) != 1 {
			return "", problem(409, "SECURITY_TRACE_INVALID", "PDF交付标记缺失或存在歧义")
		}
		token := string(matches[0][1])
		if marker != "" && marker != token {
			return "", problem(409, "SECURITY_TRACE_INVALID", "PDF页面交付标记不一致")
		}
		marker = token
	}
	return marker, nil
}

type securityPDFWriter struct{ bytes.Buffer }

func (w *securityPDFWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > securityPDFMaxBytes {
		return 0, problem(413, "SECURITY_FILE_LIMIT", "PDF水印副本超过32MiB上限")
	}
	return w.Buffer.Write(p)
}
func securityPDFWrite(ctx *model.Context) ([]byte, error) {
	var out securityPDFWriter
	if err := api.WriteContext(ctx, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func securityPDFRenderMark(text string) ([]byte, error) {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 2048 {
		return nil, invalid
	}
	parsed, err := securityPDFFont()
	if err != nil {
		return nil, fmt.Errorf("load PDF watermark font: %w", err)
	}
	var buf sfnt.Buffer
	for _, r := range text {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return nil, invalid
		}
		g, err := parsed.GlyphIndex(&buf, r)
		if err != nil {
			return nil, err
		}
		if g == 0 {
			return nil, problem(409, "SECURITY_PDF_GLYPH_UNSUPPORTED", "水印包含字体不支持的字符")
		}
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 40, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	const width = 1800
	const lineHeight = 76
	var lines []string
	line := ""
	for _, r := range text {
		if r == '\n' {
			lines = append(lines, line)
			line = ""
			continue
		}
		next := line + string(r)
		if font.MeasureString(face, next) > fixed.I(width-48) && line != "" {
			lines = append(lines, line)
			line = string(r)
		} else {
			line = next
		}
	}
	lines = append(lines, line)
	if len(lines) > 40 {
		return nil, invalid
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, len(lines)*lineHeight+32))
	drawer := font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{R: 52, G: 69, B: 88, A: 255}), Face: face}
	for i, line := range lines {
		drawer.Dot = fixed.P((width-font.MeasureString(face, line).Ceil())/2, 60+i*lineHeight)
		drawer.DrawString(line)
	}
	var out bytes.Buffer
	if err = png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
