package workflow

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

func (s *Server) registerLightweightGovernance(api, write *gin.RouterGroup) {
	guard := func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["workspace_id"]; ok {
			s.fail(c, problem(400, "LIGHTWEIGHT_WORKSPACE", "轻量治理不使用工作空间"))
			c.Abort()
			return
		}
		c.Next()
	}
	reads := api.Group("/lightweight-governance", s.require("governance", "governor"), guard)
	writes := write.Group("/lightweight-governance", s.require("governance", "governor"), guard, lightweightMutationScope)
	reads.GET("/files", s.lightweightFiles)
	reads.GET("/files/:id", s.lightweightFileDetail)
	writes.POST("/files", s.lightweightUploadFile)
	reads.GET("/units", s.lightweightUnits)
	writes.POST("/units", s.mutate("lightweight_create_unit", s.lightweightCreateUnit))
	reads.GET("/units/:id", s.lightweightUnitDetail)
	writes.POST("/units/:id", s.mutatePath("lightweight_update_unit", s.lightweightUpdateUnit))
	reads.GET("/units/:id/runs", s.lightweightUnitRuns)
	writes.POST("/units/:id/run", s.mutatePath("lightweight_run_unit", s.lightweightRunUnit))
	reads.GET("/runs/:id", s.lightweightRunRoute(s.governanceRunDetail))
	reads.GET("/runs/:id/events", s.lightweightRunRoute(s.governanceRunEvents))
	reads.GET("/runs/:id/records", s.lightweightRunRoute(s.governanceFlowRecords))
	reads.GET("/runs/:id/rejections", s.lightweightRunRoute(s.lightweightRejections))
	reads.GET("/runs/:id/download", s.lightweightRunRoute(s.lightweightDownload))
	writes.POST("/runs/:id/cancel", s.mutatePath("lightweight_cancel_run", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, err := s.lightweightRunRead(tx, p, id, true); err != nil {
			return 0, nil, id, err
		}
		return s.governanceCancelRun(tx, p, id, b)
	}))
}
func (s *Server) lightweightRunRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceRun, error) {
	run, err := governanceRunRead(tx, p, id, lock)
	if err == nil && (!lightweightRunMarker(run) || run.ActorID != p.UserID || run.DepartmentID != p.DepartmentID) {
		err = denied
	}
	return run, err
}
func (s *Server) lightweightRunRoute(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := s.lightweightRunRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); err != nil {
			s.fail(c, err)
			return
		}
		next(c)
	}
}
func lightweightPage(c *gin.Context) (int, int, error) {
	page, err := intQuery(c, "page", 1, 100000)
	if err != nil {
		return 0, 0, err
	}
	size, err := intQuery(c, "page_size", 20, 100)
	return page, size, err
}
func (s *Server) lightweightFiles(c *gin.Context) {
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	p := principal(c)
	q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind=? AND owner_id=? AND department_id=?", lightweightUploadKind, p.UserID, p.DepartmentID)
	q = lightweightScopedQuery(q, p)
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	rows := []Record{}
	if err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	items := []lightweightFileDTO{}
	for _, row := range rows {
		upload, e := decode[lightweightUpload](row.Body)
		if e != nil {
			s.fail(c, e)
			return
		}
		items = append(items, upload.File)
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) lightweightUploadFile(c *gin.Context) {
	if !fileCapacity(c) {
		return
	}
	defer func() { <-fileWork }()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lightweightMaxBytes+(1<<20))
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		s.fail(c, problem(400, "LIGHTWEIGHT_CSV_LIMIT", "上传格式错误或文件超过 20 MiB"))
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	if len(c.Request.MultipartForm.Value) > 0 || len(c.Request.MultipartForm.File) != 1 || len(c.Request.MultipartForm.File["file"]) != 1 {
		s.fail(c, invalid)
		return
	}
	header, err := c.FormFile("file")
	if err != nil || header.Size > lightweightMaxBytes || !valid(header.Filename, 255) || !strings.EqualFold(filepath.Ext(header.Filename), ".csv") {
		s.fail(c, problem(400, "LIGHTWEIGHT_CSV_REQUIRED", "请上传不超过 20 MiB 的 CSV 文件"))
		return
	}
	stream, err := header.Open()
	if err != nil {
		s.fail(c, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(stream, lightweightMaxBytes+1))
	closeErr := stream.Close()
	if err != nil {
		s.fail(c, err)
		return
	}
	if closeErr != nil {
		s.fail(c, closeErr)
		return
	}
	file, err := inspectLightweightCSV(header.Filename, data)
	if err != nil {
		s.fail(c, err)
		return
	}
	store := s.storeFile
	if principal(c).FullExperience {
		store = s.storePrivateFile
	}
	object, err := store(c.Request.Context(), file.Name, "text/csv; charset=utf-8", data)
	if err != nil {
		s.fail(c, err)
		return
	}
	p := principal(c)
	commitPending := false
	row, err := newRecord(lightweightUploadKind, p.DepartmentID, p.UserID, "", "ready", json.RawMessage("null"))
	if err == nil {
		file.FileID = row.ID
		row.Body = encode(lightweightUpload{File: file, Object: object, VisitorScope: sandboxVisitorScope(p)})
		err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if e := tx.Create(&row).Error; e != nil {
				return e
			}
			if e := s.audit(tx, p, "lightweight_upload", row.ID, "success", c.GetString("trace_id")); e != nil {
				return e
			}
			commitPending = true
			return nil
		})
	}
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Commit errors can be ambiguous. Preserve the object if metadata exists or
		// commit might still be completing; a missing object breaks historical runs.
		if row.ID != "" {
			var existing Record
			lookup := s.db.WithContext(ctx).Where("kind=? AND id=?", lightweightUploadKind, row.ID).Take(&existing).Error
			if lookup == nil || !errors.Is(lookup, gorm.ErrRecordNotFound) || commitPending {
				s.fail(c, problem(503, "UPLOAD_CONFIRMATION_UNKNOWN", "上传确认暂不可用，请重新加载文件列表"))
				return
			}
		}
		if cleanup := s.runtime.store.RemoveObject(ctx, resourceBucket, object.Key, minio.RemoveObjectOptions{}); cleanup != nil {
			s.fail(c, problem(503, "UPLOAD_CLEANUP_FAILED", "上传确认失败，暂存文件待清理"))
			return
		}
		s.fail(c, err)
		return
	}
	c.JSON(201, file)
}
func (s *Server) lightweightUnits(c *gin.Context) {
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	q := tx.Model(&Record{}).Where("kind=? AND owner_id=? AND department_id=?", lightweightUnitKind, p.UserID, p.DepartmentID)
	q = lightweightScopedQuery(q, p)
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	rows := []Record{}
	if err = q.Order("updated_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	items := []lightweightUnitDTO{}
	for _, row := range rows {
		item, e := s.lightweightUnitDTO(tx, p, row)
		if e != nil {
			s.fail(c, e)
			return
		}
		items = append(items, item)
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) lightweightUnitDetail(c *gin.Context) {
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	row, err := lightweightPrivateRecord(tx, p, lightweightUnitKind, c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	item, err := s.lightweightUnitDTO(tx, p, row)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, item)
}
func (s *Server) lightweightCreateUnit(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	input, err := bind[struct {
		Name  string             `json:"name"`
		Graph GovernanceDataflow `json:"graph"`
	}](b)
	body := lightweightUnitBody{Name: input.Name, Graph: input.Graph, VisitorScope: sandboxVisitorScope(p)}
	if err != nil {
		return 0, nil, "", err
	}
	body.Name = strings.TrimSpace(body.Name)
	if !valid(body.Name, 200) {
		return 0, nil, "", invalid
	}
	row, err := newRecord(lightweightUnitKind, p.DepartmentID, p.UserID, "", "active", body)
	if err != nil {
		return 0, nil, "", err
	}
	if _, _, err = s.prepareLightweight(tx, p, row, body); err != nil {
		return 0, nil, row.ID, err
	}
	if err = s.validateGovernanceScriptRuntimes(tx.Statement.Context, body.Graph); err != nil {
		return 0, nil, row.ID, err
	}
	if err = tx.Create(&row).Error; err != nil {
		return 0, nil, row.ID, err
	}
	item, err := s.lightweightUnitDTO(tx, p, row)
	return 201, encode(item), row.ID, err
}
func (s *Server) lightweightUpdateUnit(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	body, err := bind[struct {
		ExpectedVersion int                `json:"expected_version"`
		Name            string             `json:"name"`
		Graph           GovernanceDataflow `json:"graph"`
	}](b)
	if err != nil {
		return 0, nil, id, err
	}
	row, err := lightweightPrivateRecord(tx, p, lightweightUnitKind, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if row.Version != body.ExpectedVersion {
		return 0, nil, id, problem(409, "LIGHTWEIGHT_VERSION_CHANGED", "治理单元已更新，请重新加载")
	}
	value := lightweightUnitBody{Name: strings.TrimSpace(body.Name), Graph: body.Graph, VisitorScope: sandboxVisitorScope(p)}
	if !valid(value.Name, 200) {
		return 0, nil, id, invalid
	}
	row.Version++
	row.Body = encode(value)
	if _, _, err = s.prepareLightweight(tx, p, row, value); err != nil {
		return 0, nil, id, err
	}
	if err = s.validateGovernanceScriptRuntimes(tx.Statement.Context, value.Graph); err != nil {
		return 0, nil, id, err
	}
	if err = tx.Save(&row).Error; err != nil {
		return 0, nil, id, err
	}
	item, err := s.lightweightUnitDTO(tx, p, row)
	return 200, encode(item), id, err
}
func (s *Server) lightweightRunUnit(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	body, err := bind[struct {
		ExpectedVersion int `json:"expected_version"`
	}](b)
	if err != nil {
		return 0, nil, id, err
	}
	row, err := lightweightPrivateRecord(tx, p, lightweightUnitKind, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if row.Version != body.ExpectedVersion {
		return 0, nil, id, problem(409, "LIGHTWEIGHT_VERSION_CHANGED", "治理单元已更新，请重新加载")
	}
	value, err := decode[lightweightUnitBody](row.Body)
	if err != nil {
		return 0, nil, id, err
	}
	def, _, err := s.prepareLightweight(tx, p, row, value)
	if err != nil {
		return 0, nil, id, err
	}
	if err = s.validateGovernanceScriptRuntimes(tx.Statement.Context, value.Graph); err != nil {
		return 0, nil, id, err
	}
	if err = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; err != nil {
		return 0, nil, id, err
	}
	var pending int64
	if err = tx.Model(&GovernanceRun{}).Where("actor_id=? AND kind<>'workflow' AND state IN ('queued','running')", p.UserID).Count(&pending).Error; err != nil {
		return 0, nil, id, err
	}
	if pending >= 10 {
		return 0, nil, id, conflict
	}
	runID, err := newID()
	if err != nil {
		return 0, nil, id, err
	}
	plan := governanceEnginePlan(def, "batch")
	def.ExecutionPlan = &plan
	run := GovernanceRun{ID: runID, Kind: "dataflow", DepartmentID: p.DepartmentID, ActorID: p.UserID, Definition: encode(def), State: "queued", Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if err = tx.Create(&run).Error; err != nil {
		return 0, nil, id, err
	}
	if err = governanceRunEvent(tx, run, "FLOW_QUEUED"); err != nil {
		return 0, nil, id, err
	}
	return 201, encode(run), run.ID, nil
}
func (s *Server) lightweightUnitRuns(c *gin.Context) {
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	if _, err := lightweightPrivateRecord(tx, p, lightweightUnitKind, c.Param("id"), false); err != nil {
		s.fail(c, err)
		return
	}
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	q := tx.Model(&GovernanceRun{}).Where("actor_id=? AND department_id=? AND workspace_id IS NULL AND kind='dataflow' AND definition->'lightweight'->>'unit_id'=?", p.UserID, p.DepartmentID, c.Param("id"))
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	items := []GovernanceRun{}
	if err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) lightweightRejections(c *gin.Context) {
	ctx := c.Request.Context()
	p := principal(c)
	run, def, report, _, err := s.exportBasis(s.db.WithContext(ctx), p, c.Param("id"))
	if err != nil {
		s.fail(c, err)
		return
	}
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !validGovernanceStorage(report.Storage, run.ID) {
		s.fail(c, invalid)
		return
	}
	// Quarantine is internal evidence. Use the service identity only after the
	// run owner and pinned inputs have been authorized above.
	db, err := dorisOpen(ctx, true)
	if err != nil {
		s.fail(c, err)
		return
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT node_id,ordinal,payload,violations FROM "+dorisDatabase("quality")+".rejected_"+run.ID+" ORDER BY node_id,ordinal LIMIT "+fmt.Sprint(size)+" OFFSET "+fmt.Sprint((page-1)*size))
	if err != nil {
		s.fail(c, err)
		return
	}
	defer rows.Close()
	type rejection struct {
		NodeID     string   `json:"node_id"`
		Ordinal    int64    `json:"ordinal"`
		Data       DataRow  `json:"data"`
		Violations []string `json:"violations"`
	}
	items := []rejection{}
	for rows.Next() {
		item := rejection{}
		var data, violations []byte
		if err = rows.Scan(&item.NodeID, &item.Ordinal, &data, &violations); err != nil {
			s.fail(c, err)
			return
		}
		if item.Data, err = decode[DataRow](data); err != nil {
			s.fail(c, err)
			return
		}
		if item.Violations, err = decode[[]string](violations); err != nil {
			s.fail(c, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		s.fail(c, err)
		return
	}
	if err = s.governanceFlowAuthorize(ctx, p, def); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": report.RejectedRows, "page": page, "page_size": size})
}
func (s *Server) lightweightDownload(c *gin.Context) {
	ctx := c.Request.Context()
	p := principal(c)
	run, def, report, fields, err := s.exportBasis(s.db.WithContext(ctx), p, c.Param("id"))
	if err != nil {
		s.fail(c, err)
		return
	}
	file, err := os.CreateTemp("", "datamesh-lightweight-*.csv")
	if err != nil {
		s.fail(c, err)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	writer := csv.NewWriter(file)
	headers := []string{}
	for _, field := range fields {
		label := report.FieldLabels[field.Name]
		if label == "" {
			label = field.Name
		}
		headers = append(headers, label)
	}
	if err = writer.Write(headers); err != nil {
		s.fail(c, err)
		return
	}
	for offset := 0; int64(offset) < report.OutputRows; offset += 250 {
		if err = s.governanceFlowAuthorize(ctx, p, def); err != nil {
			s.fail(c, err)
			return
		}
		rows, e := dorisResultPage(ctx, report.Storage, run.ID, fields, offset, 250)
		if e != nil {
			s.fail(c, e)
			return
		}
		if len(rows) != min(250, int(report.OutputRows)-offset) {
			s.fail(c, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果行数变化"))
			return
		}
		for _, row := range rows {
			values := []string{}
			for _, field := range fields {
				raw := row[field.Name]
				text := ""
				if string(raw) != "null" {
					if json.Unmarshal(raw, &text) != nil {
						text = string(raw)
					}
				}
				values = append(values, text)
			}
			if err = writer.Write(values); err != nil {
				s.fail(c, err)
				return
			}
		}
		writer.Flush()
		if err = writer.Error(); err != nil {
			s.fail(c, err)
			return
		}
		info, e := file.Stat()
		if e != nil {
			s.fail(c, e)
			return
		}
		if info.Size() > s.etl.MaxBytes {
			s.fail(c, problem(409, "GOVERNANCE_LIMIT", "下载数据超过治理字节上限"))
			return
		}
	}
	writer.Flush()
	if err = writer.Error(); err != nil {
		s.fail(c, err)
		return
	}
	if err = s.governanceFlowAuthorize(ctx, p, def); err != nil {
		s.fail(c, err)
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		s.fail(c, err)
		return
	}
	info, err := file.Stat()
	if err != nil {
		s.fail(c, err)
		return
	}
	if err = s.audit(s.db.WithContext(ctx), p, "lightweight_download", run.ID, "success", c.GetString("trace_id")); err != nil {
		s.fail(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "governance-" + run.ID + ".csv"}))
	c.Header("Content-Length", fmt.Sprint(info.Size()))
	c.Status(200)
	if _, err = io.Copy(c.Writer, file); err != nil {
		slog.Warn("lightweight CSV response interrupted", "run_id", run.ID)
	}
}

func (s *Server) lightweightFileDetail(c *gin.Context) {
	_, upload, err := lightweightUploadRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, upload.File)
}
