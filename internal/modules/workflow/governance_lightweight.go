package workflow

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

const lightweightUploadKind = "lightweight_upload"
const lightweightUnitKind = "lightweight_unit"
const lightweightMaxBytes = 20 << 20
const lightweightMaxRows = 100000

type lightweightFileDTO struct {
	FileID      string            `json:"file_id"`
	Name        string            `json:"name"`
	Size        int64             `json:"size"`
	Rows        int               `json:"rows"`
	Fields      []Field           `json:"fields"`
	FieldLabels map[string]string `json:"field_labels"`
	Version     int               `json:"version"`
}
type lightweightUpload struct {
	File         lightweightFileDTO `json:"file"`
	Object       FileConfig         `json:"object"`
	Canonical    *FileConfig        `json:"canonical,omitempty"`
	VisitorScope string             `json:"visitor_scope,omitempty"`
}
type lightweightUnitBody struct {
	VisitorScope string             `json:"visitor_scope,omitempty"`
	Name         string             `json:"name"`
	Graph        GovernanceDataflow `json:"graph"`
}
type lightweightUnitDTO struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Version   int                  `json:"version"`
	Graph     GovernanceDataflow   `json:"graph"`
	Files     []lightweightFileDTO `json:"files"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
}
type governanceLightweightExecution struct {
	VisitorScope    string `json:"visitor_scope,omitempty"`
	UnitFingerprint string `json:"unit_fingerprint,omitempty"`
	UnitID          string `json:"unit_id"`
	Version         int    `json:"version"`
}

// encoding/csv removes one CR before every LF, including inside quoted text.
// Protect quoted CRLF while keeping the stored object and its digest unchanged.
func lightweightCSVText(data []byte) []byte {
	var out bytes.Buffer
	quoted := false
	for i := 0; i < len(data); i++ {
		value := data[i]
		if value == '"' {
			if quoted && i+1 < len(data) && data[i+1] == '"' {
				out.WriteByte(value)
				out.WriteByte(value)
				i++
				continue
			}
			quoted = !quoted
		}
		if quoted && value == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			out.WriteByte('\r')
		}
		out.WriteByte(value)
	}
	return out.Bytes()
}
func parseLightweightCSV(name string, data []byte) (lightweightFileDTO, [][]string, error) {
	return parseLightweightCSVRows(name, data, true)
}
func inspectLightweightCSV(name string, data []byte) (lightweightFileDTO, error) {
	file, _, err := parseLightweightCSVRows(name, data, false)
	return file, err
}
func parseLightweightCSVRows(name string, data []byte, collectRows bool) (lightweightFileDTO, [][]string, error) {
	file := lightweightFileDTO{Name: filepath.Base(name), Size: int64(len(data)), Fields: []Field{}, FieldLabels: map[string]string{}, Version: 1}
	if len(data) > lightweightMaxBytes {
		return file, nil, problem(400, "LIGHTWEIGHT_CSV_LIMIT", "CSV 文件不能超过 20 MiB")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return file, nil, problem(400, "LIGHTWEIGHT_CSV_ENCODING", "CSV 须使用 UTF-8 编码")
	}
	data = lightweightCSVText(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if bytes.HasPrefix(data, []byte("\n")) || bytes.HasPrefix(data, []byte("\r\n")) {
		return file, nil, problem(400, "LIGHTWEIGHT_CSV_HEADER", "CSV 首行不能为空")
	}
	reader := csv.NewReader(bytes.NewReader(data))
	headers, err := reader.Read()
	if err != nil || len(headers) == 0 || len(headers) > 100 {
		return file, nil, problem(400, "LIGHTWEIGHT_CSV_HEADER", "CSV 首行须包含 1–100 个字段")
	}
	labels := map[string]bool{}
	reserved := map[string]bool{}
	for _, label := range headers {
		if strings.TrimSpace(label) == "" || labels[label] || len(label) > 1000 {
			return file, nil, problem(400, "LIGHTWEIGHT_CSV_HEADER", "CSV 字段名不能为空或重复，且不能超过 1000 字节")
		}
		labels[label] = true
		if governanceIdentifier.MatchString(label) {
			reserved[strings.ToLower(label)] = true
		}
	}
	assigned := map[string]bool{}
	for i, label := range headers {
		key := label
		if !governanceIdentifier.MatchString(key) || assigned[strings.ToLower(key)] {
			key = fmt.Sprintf("column_%d", i+1)
			for suffix := 1; reserved[strings.ToLower(key)]; suffix++ {
				key = fmt.Sprintf("column_%d_%d", i+1, suffix)
			}
			reserved[strings.ToLower(key)] = true
		}
		assigned[strings.ToLower(key)] = true
		file.Fields = append(file.Fields, Field{Name: key, LogicalType: "string", Nullable: true})
		file.FieldLabels[key] = label
	}
	rows := [][]string{}
	count := 0
	reader.ReuseRecord = !collectRows
	for {
		remaining := data[reader.InputOffset():]
		if bytes.HasPrefix(remaining, []byte("\n")) || bytes.HasPrefix(remaining, []byte("\r\n")) {
			return file, nil, problem(400, "LIGHTWEIGHT_CSV_ROW", fmt.Sprintf("CSV 第 %d 条记录为空，请显式填写空字段", count+2))
		}
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return file, nil, problem(400, "LIGHTWEIGHT_CSV_ROW", fmt.Sprintf("CSV 第 %d 条记录格式错误或字段数量不一致", count+2))
		}
		if count >= lightweightMaxRows {
			return file, nil, problem(400, "LIGHTWEIGHT_CSV_LIMIT", fmt.Sprintf("CSV 数据不能超过 %d 行", lightweightMaxRows))
		}
		count++
		if collectRows {
			rows = append(rows, row)
		}
	}
	file.Rows = count
	return file, rows, nil
}

func validateLightweightGraph(graph GovernanceDataflow) error {
	sources := 0
	for _, node := range graph.Nodes {
		if !slices.Contains([]string{"source", "project", "transform", "filter", "deduplicate", "join", "quality", "script", "output"}, node.Kind) || node.Template != nil || node.Model != nil || node.Code != nil {
			return problem(400, "LIGHTWEIGHT_GRAPH", "轻量治理仅支持上传文件和内联清洗、关联、脚本及质量规则")
		}
		if node.Kind == "script" {
			if err := validateGovernanceScript(node.Script); err != nil {
				return err
			}
		} else if node.Script != nil {
			return problem(400, "LIGHTWEIGHT_GRAPH", "脚本配置须放在代码节点中")
		}
		if node.Kind == "source" {
			sources++
			if node.Source == nil || node.Source.Resource.Type != "file_resource" || node.Source.Resource.Version != 1 || len(node.Source.Parameters) > 0 || len(node.Source.ParameterBindings) > 0 {
				return problem(400, "LIGHTWEIGHT_SOURCE", "请选择自己上传的 CSV 文件")
			}
		}
		if node.Kind == "output" && !node.InferModel {
			return problem(400, "LIGHTWEIGHT_OUTPUT", "轻量治理输出须使用自动推导字段")
		}
	}
	if sources < 1 || sources > 8 {
		return problem(400, "LIGHTWEIGHT_SOURCE_LIMIT", "轻量治理需要 1–8 个文件来源")
	}
	return nil
}
func lightweightPrivateRecord(tx *gorm.DB, p identity.Principal, kind, id string, lock bool) (Record, error) {
	return lightweightPrivateRecordWithScope(tx, p, kind, id, lock, sandboxVisitorScope(p), p.FullExperience && p.CSRFToken == "")
}
func lightweightPrivateRecordWithScope(tx *gorm.DB, p identity.Principal, kind, id string, lock bool, scope string, missing bool) (Record, error) {
	row, err := get(tx, kind, id, lock)
	if err == nil && (row.OwnerID != p.UserID || row.DepartmentID != p.DepartmentID) {
		err = denied
	}
	if err == nil {
		var body struct {
			VisitorScope string `json:"visitor_scope"`
		}
		body, err = decode[struct {
			VisitorScope string `json:"visitor_scope"`
		}](row.Body)
		if err == nil && (missing || body.VisitorScope != scope) {
			err = denied
		}
	}
	return row, err
}
func lightweightUploadRead(tx *gorm.DB, p identity.Principal, id string) (Record, lightweightUpload, error) {
	row, err := lightweightPrivateRecord(tx, p, lightweightUploadKind, id, false)
	if err != nil {
		return row, lightweightUpload{}, err
	}
	upload, err := decode[lightweightUpload](row.Body)
	return row, upload, err
}
func lightweightInputBasis(tx *gorm.DB, p identity.Principal, ref Resource, names []string) (governanceInput, error) {
	if p.FullExperience && p.CSRFToken == "" {
		return governanceInput{}, denied
	}
	return lightweightInputBasisWithScope(tx, p, ref, names, sandboxVisitorScope(p))
}
func lightweightInputBasisWithScope(tx *gorm.DB, p identity.Principal, ref Resource, names []string, scope string) (governanceInput, error) {
	if ref.Type != "file_resource" || ref.Version != 1 {
		return governanceInput{}, denied
	}
	row, err := lightweightPrivateRecordWithScope(tx, p, lightweightUploadKind, ref.ID, false, scope, false)
	var upload lightweightUpload
	if err == nil {
		upload, err = decode[lightweightUpload](row.Body)
	}
	if err != nil {
		return governanceInput{}, err
	}
	if !validFields(names, upload.File.Fields) {
		return governanceInput{}, invalid
	}
	fields := []Field{}
	for _, name := range names {
		for _, field := range upload.File.Fields {
			if field.Name == name {
				fields = append(fields, field)
			}
		}
	}
	return governanceInput{VisitorScope: scope, Resource: ref, Fields: fields, DepartmentID: p.DepartmentID, Administrator: admin(p), ResourceFingerprint: fingerprint(row.Body), UploadID: row.ID, SourceName: upload.File.Name, FieldLabels: upload.File.FieldLabels}, nil
}
func (s *Server) readLightweightInput(ctx context.Context, basis governanceInput, limit int, consume func([]interface{}) error) (int64, error) {
	row, err := get(s.db.WithContext(ctx), lightweightUploadKind, basis.UploadID, false)
	if err != nil {
		return 0, err
	}
	if basis.UploadID != basis.Resource.ID || row.DepartmentID != basis.DepartmentID || fingerprint(row.Body) != basis.ResourceFingerprint {
		return 0, denied
	}
	upload, err := decode[lightweightUpload](row.Body)
	if err != nil {
		return 0, err
	}
	data, err := s.readStoredFile(ctx, upload.Object, lightweightMaxBytes)
	if err != nil {
		return 0, err
	}
	var file lightweightFileDTO
	var rows [][]string
	if upload.Canonical != nil {
		file, err = inspectLightweightCSV(upload.File.Name, data)
	} else {
		file, rows, err = parseLightweightCSV(upload.File.Name, data)
	}
	data = nil
	file.FileID = upload.File.FileID
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(file, upload.File) {
		return 0, problem(409, "LIGHTWEIGHT_INPUT_CHANGED", "上传文件结构发生变化")
	}
	indexes := []int{}
	for _, f := range basis.Fields {
		index := slices.IndexFunc(file.Fields, func(other Field) bool { return f == other })
		if index < 0 {
			return 0, invalid
		}
		indexes = append(indexes, index)
	}
	var canonical [][]interface{}
	if upload.Canonical != nil {
		canonical, err = s.readSandboxCanonical(ctx, *upload.Canonical, len(file.Fields), file.Rows)
		if err != nil {
			return 0, err
		}
	}
	for i := 0; i < file.Rows; i++ {
		if i >= limit {
			break
		}
		values := make([]interface{}, len(indexes))
		for j, index := range indexes {
			if upload.Canonical != nil {
				values[j] = canonical[i][index]
			} else {
				values[j] = rows[i][index]
			}
		}
		if err = consume(values); err != nil {
			return int64(file.Rows), err
		}
	}
	return int64(file.Rows), nil
}
func lightweightRunMarker(run GovernanceRun) bool {
	if run.Kind != "dataflow" || run.WorkspaceID != "" {
		return false
	}
	def, err := decode[governanceFlowExecution](run.Definition)
	return err == nil && def.Lightweight != nil && valid(def.Lightweight.UnitID, 128) && def.Lightweight.Version > 0 && def.Flow.ID == def.Lightweight.UnitID && def.Flow.Version == def.Lightweight.Version
}
func (s *Server) authorizeLightweightFlow(ctx context.Context, p identity.Principal, def governanceFlowExecution) error {
	if def.Lightweight == nil || def.Preview != nil || def.StreamSnapshot != nil || len(def.Parameters) > 0 || len(def.Pinned) != 1 || def.Pinned[0].Definition.Dataflow == nil || def.Flow != (GovernanceRef{ID: def.Lightweight.UnitID, Version: def.Lightweight.Version}) || def.Pinned[0].Ref != def.Flow || def.Pinned[0].Kind != "dataflow" {
		return denied
	}
	if err := validateLightweightGraph(*def.Pinned[0].Definition.Dataflow); err != nil {
		return err
	}
	unit, err := lightweightPrivateRecordWithScope(s.db.WithContext(ctx), p, lightweightUnitKind, def.Lightweight.UnitID, false, def.Lightweight.VisitorScope, false)
	if err != nil {
		return err
	}
	if unit.Version < def.Lightweight.Version || (unit.Version == def.Lightweight.Version && fingerprint(unit.Body) != def.Lightweight.UnitFingerprint) {
		return denied
	}
	count := 0
	for _, node := range def.Pinned[0].Definition.Dataflow.Nodes {
		if node.Source == nil {
			continue
		}
		count++
		input, ok := def.Inputs[node.ID]
		if !ok || input.VisitorScope != def.Lightweight.VisitorScope || input.UploadID == "" || input.Resource != node.Source.Resource || !slices.Equal(fieldNames(input.Fields), node.Source.Fields) {
			return denied
		}
		if err := s.governanceCheckInput(ctx, p, input, true); err != nil {
			return err
		}
	}
	if count != len(def.Inputs) {
		return denied
	}
	return nil
}

func (s *Server) prepareLightweight(tx *gorm.DB, p identity.Principal, row Record, body lightweightUnitBody) (governanceFlowExecution, []lightweightFileDTO, error) {
	ref := GovernanceRef{ID: row.ID, Version: row.Version}
	def := governanceFlowExecution{Lightweight: &governanceLightweightExecution{UnitID: row.ID, Version: row.Version, VisitorScope: body.VisitorScope, UnitFingerprint: fingerprint(row.Body)}, Flow: ref, Pinned: []governanceFlowPinned{{Kind: "dataflow", Ref: ref, Definition: GovernanceDefinition{Name: body.Name, OwnerID: p.UserID, StewardID: p.UserID, Dataflow: &body.Graph}}}, Inputs: map[string]governanceInput{}}
	if err := validateLightweightGraph(body.Graph); err != nil {
		return def, nil, err
	}
	files := []lightweightFileDTO{}
	seen := map[string]bool{}
	for _, node := range body.Graph.Nodes {
		if node.Source == nil {
			continue
		}
		input, err := lightweightInputBasis(tx, p, node.Source.Resource, node.Source.Fields)
		if err != nil {
			return def, nil, err
		}
		def.Inputs[node.ID] = input
		if !seen[input.UploadID] {
			_, upload, err := lightweightUploadRead(tx, p, input.UploadID)
			if err != nil {
				return def, nil, err
			}
			seen[input.UploadID] = true
			files = append(files, upload.File)
		}
	}
	if _, _, _, err := governanceExecutionPlan(def); err != nil {
		return def, nil, err
	}
	return def, files, nil
}
func (s *Server) lightweightUnitDTO(tx *gorm.DB, p identity.Principal, row Record) (lightweightUnitDTO, error) {
	body, err := decode[lightweightUnitBody](row.Body)
	if err != nil {
		return lightweightUnitDTO{}, err
	}
	_, files, err := s.prepareLightweight(tx, p, row, body)
	return lightweightUnitDTO{ID: row.ID, Name: body.Name, Version: row.Version, Graph: body.Graph, Files: files, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, err
}

func lightweightScopeMatches(p identity.Principal, scope string) bool {
	if p.FullExperience {
		return p.CSRFToken != "" && scope != "" && scope == sandboxVisitorScope(p)
	}
	return scope == ""
}
func lightweightScopedQuery(q *gorm.DB, p identity.Principal) *gorm.DB {
	scope := sandboxVisitorScope(p)
	if p.FullExperience && scope == "" {
		return q.Where("1=0")
	}
	return q.Where("COALESCE(body->>'visitor_scope','') = ?", scope)
}
