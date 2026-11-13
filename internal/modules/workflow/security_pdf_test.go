package workflow

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func securityPDFFixture(pages int, extra string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R " + extra + " >>", ""}
	var kids []string
	for i := 0; i < pages; i++ {
		n := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", n))
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Contents %d 0 R >>", n+1), "<< /Length 4 >>\nstream\nq Q\nendstream")
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages)
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	offsets := []int{0}
	for i, o := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, n := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", n)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	return out.Bytes()
}

func TestSecurityPDFWatermarkAndTrace(t *testing.T) {
	original := securityPDFFixture(2, "")
	marked, err := securityMarkPDF(original, "仅限授权使用 · 张三 · 数据部门", "1234567890abcdef1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(original, marked) {
		t.Fatal("PDF content did not change")
	}
	ctx, err := securityPDFRead(marked)
	if err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 2; page++ {
		d, _, _, err := ctx.PageDict(page, false)
		if err != nil {
			t.Fatal(err)
		}
		content, err := ctx.PageContent(d, page)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(content, []byte("Do")) {
			t.Fatalf("page %d missing image overlay", page)
		}
	}
	marker := "dm1.1234567890abcdef1234567890abcdef." + strings.Repeat("a", 64)
	traced, err := securityTracePDF(marked, marker)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := securityExtractPDFMarker(traced)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != marker {
		t.Fatalf("marker mismatch: %q", recovered)
	}
	if _, err = securityExtractPDFMarker(marked); err == nil {
		t.Fatal("unmarked PDF accepted")
	}
	if _, err = securityTracePDF(traced, marker); err == nil {
		t.Fatal("ambiguous repeated trace accepted")
	}
}
func TestSecurityPDFRejectsUnsafeInput(t *testing.T) {
	for _, body := range [][]byte{nil, []byte("not a PDF"), []byte("%PDF-1.7\nmalformed"), securityPDFFixture(201, ""), securityPDFFixture(1, "/Type /Sig /ByteRange [0 0 0 0]")} {
		if _, err := securityMarkPDF(body, "mark", "proof-12345678"); err == nil {
			t.Fatal("unsafe PDF accepted")
		}
		if _, err := securityTracePDF(body, "proof-12345678"); err == nil {
			t.Fatal("unsafe trace input accepted")
		}
	}
	if _, err := securityMarkPDF(securityPDFFixture(1, ""), "mark", "x) Q ("); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
	if _, err := securityPDFRenderMark("unsupported \U0010ffff"); err == nil {
		t.Fatal("missing glyph silently accepted")
	}
	if _, err := securityPDFRenderMark("mark\x00"); err == nil {
		t.Fatal("control character accepted")
	}
}

func TestSecurityPDFRejectsEncryption(t *testing.T) {
	var encrypted bytes.Buffer
	conf := securityPDFConfig()
	conf.OwnerPW = "owner-test-password"
	if err := api.Encrypt(bytes.NewReader(securityPDFFixture(1, "")), &encrypted, conf); err != nil {
		t.Fatal(err)
	}
	// Empty user passwords permit opening in normal viewers, but the file
	// still carries encryption and must never be silently decrypted here.
	if _, err := securityMarkPDF(encrypted.Bytes(), "mark", "proof-12345678"); err == nil {
		t.Fatal("encrypted PDF accepted")
	}
	if _, err := securityTracePDF(encrypted.Bytes(), "proof-12345678"); err == nil {
		t.Fatal("encrypted PDF trace accepted")
	}
}

func TestSecurityPDFTraceSurvivesSerialization(t *testing.T) {
	marker := "dm1.1234567890abcdef1234567890abcdef." + strings.Repeat("b", 64)
	traced, err := securityTracePDF(securityPDFFixture(2, ""), marker)
	if err != nil {
		t.Fatal(err)
	}
	var rewritten bytes.Buffer
	if err = api.Optimize(bytes.NewReader(traced), &rewritten, securityPDFConfig()); err != nil {
		t.Fatal(err)
	}
	got, err := securityExtractPDFMarker(rewritten.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got != marker {
		t.Fatal("trace lost after PDF serialization")
	}
}

func TestSecurityPDFRejectsDecodedContentBomb(t *testing.T) {
	ctx, err := securityPDFRead(securityPDFFixture(3, ""))
	if err != nil {
		t.Fatal(err)
	}
	// Three references to a tiny compressed representation must still count
	// as 72MiB of decoded page content against the document-wide budget.
	ref, err := ctx.StreamDictIndRef(bytes.Repeat([]byte(" "), 24<<20))
	if err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 3; page++ {
		d, _, _, err := ctx.PageDict(page, false)
		if err != nil {
			t.Fatal(err)
		}
		d["Contents"] = *ref
	}
	body, err := securityPDFWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = securityTracePDF(body, "proof-12345678"); err == nil {
		t.Fatal("decoded content bomb accepted")
	}
}
