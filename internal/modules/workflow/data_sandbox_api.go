package workflow

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func sandboxReserve(tx *gorm.DB, box DataSandbox, name string, size int64) error {
	if !sandboxIdentifier.MatchString(name) {
		return invalid
	}
	var duplicate int64
	if err := tx.Model(&DataSandboxTable{}).Where("sandbox_id=? AND lower(name)=lower(?) AND state<>'removed'", box.ID, name).Count(&duplicate).Error; err != nil {
		return err
	}
	if duplicate > 0 {
		return problem(409, "SANDBOX_TABLE_EXISTS", "表名已被占用")
	}
	var count int64
	var used int64
	q := tx.Model(&DataSandboxTable{}).Where("sandbox_id=? AND state<>'removed'", box.ID)
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if err := q.Select("COALESCE(SUM(bytes),0)").Scan(&used).Error; err != nil {
		return err
	}
	var previews int64
	if err := tx.Model(&DataSandboxQuery{}).Where("sandbox_id=?", box.ID).Select("COALESCE(SUM(reserved_bytes),0)").Scan(&previews).Error; err != nil {
		return err
	}
	var pending int64
	if err := tx.Raw(`SELECT COALESCE(SUM((o.object->>'size')::bigint),0) FROM data_sandbox_objects o WHERE o.sandbox_id=? AND NOT EXISTS(SELECT 1 FROM data_sandbox_tables t WHERE t.source_object->>'key'=o.key OR t.result_object->>'key'=o.key) AND NOT EXISTS(SELECT 1 FROM data_sandbox_queries q WHERE q.result_object->>'key'=o.key)`, box.ID).Scan(&pending).Error; err != nil {
		return err
	}
	if count >= sandboxMaxTables || used+previews+pending+size > sandboxMaxBytes {
		return problem(409, "SANDBOX_LIMIT", "沙箱表数量或容量已达上限")
	}
	return nil
}
func sandboxNewTable(box DataSandbox, name string, fields []Field, labels map[string]string, size int64) (DataSandboxTable, error) {
	id, err := newID()
	now := time.Now().UTC()
	return DataSandboxTable{ID: id, SandboxID: box.ID, Name: name, State: "queued", Fields: encode(fields), FieldLabels: encode(labels), Bytes: size, PhysicalTable: "t_" + id, SandboxVersion: &box.Version, CreatedAt: now, UpdatedAt: now}, err
}
func (s *Server) sandboxTables(c *gin.Context) {
	if _, err := sandboxRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); err != nil {
		s.fail(c, err)
		return
	}
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	items := []DataSandboxTable{}
	q := s.db.WithContext(c.Request.Context()).Model(&DataSandboxTable{}).Where("sandbox_id=? AND state<>'removed'", c.Param("id"))
	var total int64
	if err = q.Count(&total).Error; err == nil {
		err = q.Order("created_at,id").Offset((page - 1) * size).Limit(size).Find(&items).Error
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) sandboxTable(c *gin.Context) (DataSandboxTable, error) {
	var table DataSandboxTable
	if _, err := sandboxRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); err != nil {
		return table, err
	}
	err := s.db.WithContext(c.Request.Context()).Where("sandbox_id=? AND id=? AND state<>'removed'", c.Param("id"), c.Param("table_id")).Take(&table).Error
	return table, err
}
func (s *Server) sandboxTableDetail(c *gin.Context) {
	table, err := s.sandboxTable(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, table)
}
func (s *Server) sandboxUpload(c *gin.Context) {
	if !fileCapacity(c) {
		return
	}
	defer func() { <-fileWork }()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lightweightMaxBytes+(1<<20))
	h, err := c.FormFile("file")
	if err != nil || h.Size > lightweightMaxBytes {
		s.fail(c, invalid)
		return
	}
	stream, err := h.Open()
	if err != nil {
		s.fail(c, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(stream, lightweightMaxBytes+1))
	closeErr := stream.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	file, err := inspectLightweightCSV(h.Filename, data)
	if err != nil {
		s.fail(c, err)
		return
	}
	var table DataSandboxTable
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		box, e := sandboxRead(tx, principal(c), c.Param("id"), true)
		if e != nil {
			return e
		}
		name := strings.TrimSpace(c.PostForm("name"))
		if e = sandboxReserve(tx, box, name, int64(len(data))); e != nil {
			return e
		}
		object, e := s.sandboxStoreFile(sandboxObjectContext(c.Request.Context(), box.ID), file.Name, "text/csv", data)
		if e != nil {
			return e
		}
		table, e = sandboxNewTable(box, name, file.Fields, file.FieldLabels, int64(len(data)))
		if e != nil {
			return e
		}
		table.Rows = int64(file.Rows)
		table.SourceObject = encode(object)
		if e = tx.Create(&table).Error; e != nil {
			return e
		}
		return s.audit(tx, principal(c), "sandbox_upload", table.ID, "success", c.GetString("trace_id"))
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(201, table)
}
func (s *Server) sandboxTableRows(c *gin.Context) (sandboxRows, error) {
	table, err := s.sandboxTable(c)
	if err != nil {
		return sandboxRows{}, err
	}
	if table.State != "ready" {
		return sandboxRows{}, conflict
	}
	var object FileConfig
	if err = json.Unmarshal(table.ResultObject, &object); err != nil {
		return sandboxRows{}, err
	}
	return s.sandboxReadObject(c.Request.Context(), object)
}
func sandboxRecords(c *gin.Context, rows sandboxRows) {
	page, size, err := lightweightPage(c)
	if err != nil {
		c.JSON(400, gin.H{"code": "INVALID_INPUT"})
		return
	}
	start := (page - 1) * size
	if start > len(rows.Values) {
		start = len(rows.Values)
	}
	end := start + size
	if end > len(rows.Values) {
		end = len(rows.Values)
	}
	items := []map[string]interface{}{}
	for _, row := range rows.Values[start:end] {
		item := map[string]interface{}{}
		for i, f := range rows.Fields {
			item[f.Name] = row[i]
		}
		items = append(items, item)
	}
	c.JSON(200, gin.H{"items": items, "total": len(rows.Values), "fields": rows.Fields, "truncated": rows.Truncated, "page": page, "page_size": size})
}
func (s *Server) sandboxTableRecords(c *gin.Context) {
	rows, err := s.sandboxTableRows(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	sandboxRecords(c, rows)
}
func (s *Server) sandboxDownload(c *gin.Context) {
	rows, err := s.sandboxTableRows(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := []string{}
	for _, f := range rows.Fields {
		label := rows.FieldLabels[f.Name]
		if label == "" {
			label = f.Name
		}
		header = append(header, label)
	}
	_ = w.Write(header)
	for _, row := range rows.Values {
		record := make([]string, len(row))
		for i, v := range row {
			if v != nil {
				record[i] = v.(string)
			}
		}
		if err = w.Write(record); err != nil {
			s.fail(c, err)
			return
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		s.fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="sandbox.csv"`)
	c.Header("X-Sandbox-CSV-Null", "empty-field")
	c.Data(200, "text/csv; charset=utf-8", b.Bytes())
}
func (s *Server) sandboxHandoffCreate(tx *gorm.DB, p identity.Principal, id, tableID string, body []byte) (int, json.RawMessage, string, error) {
	var file lightweightFileDTO

	err := func() error {
		box, e := sandboxRead(tx, p, id, true)
		if e != nil {
			return e
		}
		if e = s.sandboxActor(tx.Statement.Context, box); e != nil {
			return e
		}
		var table DataSandboxTable
		if e = tx.Where("sandbox_id=? AND id=? AND state='ready'", box.ID, tableID).Take(&table).Error; e != nil {
			return e
		}
		var object FileConfig
		if e = json.Unmarshal(table.ResultObject, &object); e != nil {
			return e
		}
		rows, e := s.sandboxReadObject(tx.Statement.Context, object)
		if e != nil {
			return e
		}
		bound := *s
		bound.db = tx
		file, e = bound.createSandboxLightweightFile(tx.Statement.Context, p, table.Name+".csv", rows)
		if e != nil {
			return e
		}
		return nil
	}()
	if err != nil {
		return 0, nil, "", err
	}
	return 201, encode(file), file.FileID, nil
}

var sandboxIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func (s *Server) sandboxTableRemove(c *gin.Context) {
	var table DataSandboxTable
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, e := sandboxRead(tx, principal(c), c.Param("id"), true); e != nil {
			return e
		}
		if e := tx.Where("sandbox_id=? AND id=?", c.Param("id"), c.Param("table_id")).Take(&table).Error; e != nil {
			return e
		}
		if table.State == "running" || table.State == "unknown" {
			return conflict
		}
		table.State = "removing"
		table.CancelRequested = true
		table.UpdatedAt = time.Now().UTC()
		if e := tx.Save(&table).Error; e != nil {
			return e
		}
		return s.audit(tx, principal(c), "sandbox_remove", table.ID, "success", c.GetString("trace_id"))
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, table)
}
func (s *Server) sandboxImportFile(c *gin.Context) {
	if !fileCapacity(c) {
		return
	}
	defer func() { <-fileWork }()
	var in struct {
		FileID string `json:"file_id"`
		Name   string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil {
		s.fail(c, invalid)
		return
	}
	var table DataSandboxTable
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		box, e := sandboxRead(tx, principal(c), c.Param("id"), true)
		if e != nil {
			return e
		}
		_, upload, e := lightweightUploadRead(tx, principal(c), in.FileID)
		if e != nil {
			return e
		}
		if e = sandboxReserve(tx, box, in.Name, upload.File.Size); e != nil {
			return e
		}
		// Copy the source so sandbox retention cannot delete the independent upload.
		data, e := s.readStoredFile(c.Request.Context(), upload.Object, lightweightMaxBytes)
		if e != nil {
			return e
		}
		object, e := s.sandboxStoreFile(sandboxObjectContext(c.Request.Context(), box.ID), upload.File.Name, "text/csv", data)
		if e != nil {
			return e
		}
		table, e = sandboxNewTable(box, in.Name, upload.File.Fields, upload.File.FieldLabels, upload.File.Size)
		if e != nil {
			return e
		}
		table.SourceObject = encode(object)
		table.Rows = int64(upload.File.Rows)
		if upload.Canonical != nil {
			values, e := s.readSandboxCanonical(c.Request.Context(), *upload.Canonical, len(upload.File.Fields), upload.File.Rows)
			if e != nil {
				return e
			}
			encoded, e := json.Marshal(sandboxObject{Fields: upload.File.Fields, FieldLabels: upload.File.FieldLabels, Values: values})
			if e != nil {
				return e
			}
			if int64(len(encoded)) > table.Bytes {
				table.Bytes = int64(len(encoded))
				if e = sandboxReserve(tx, box, in.Name, table.Bytes); e != nil {
					return e
				}
			}
			canonical, e := s.sandboxStoreObject(sandboxObjectContext(c.Request.Context(), box.ID), sandboxRows{Fields: upload.File.Fields, FieldLabels: upload.File.FieldLabels, Values: values})
			if e != nil {
				return e
			}
			table.ResultObject = encode(canonical)
		}
		if e := tx.Create(&table).Error; e != nil {
			return e
		}
		return s.audit(tx, principal(c), "sandbox_import", table.ID, "success", c.GetString("trace_id"))
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(201, table)
}
func (s *Server) sandboxQueries(c *gin.Context) {
	if _, err := sandboxRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); err != nil {
		s.fail(c, err)
		return
	}
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	items := []DataSandboxQuery{}
	var total int64
	q := s.db.WithContext(c.Request.Context()).Model(&DataSandboxQuery{}).Where("sandbox_id=?", c.Param("id"))
	if err = q.Count(&total).Error; err == nil {
		err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&items).Error
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) sandboxQuery(c *gin.Context) (DataSandboxQuery, error) {
	var query DataSandboxQuery
	if _, err := sandboxRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); err != nil {
		return query, err
	}
	err := s.db.Where("sandbox_id=? AND id=?", c.Param("id"), c.Param("query_id")).Take(&query).Error
	return query, err
}
func (s *Server) sandboxQueryDetail(c *gin.Context) {
	q, e := s.sandboxQuery(c)
	if e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, q)
}
func (s *Server) sandboxQueryRecords(c *gin.Context) {
	q, e := s.sandboxQuery(c)
	if e != nil {
		s.fail(c, e)
		return
	}
	if q.State != "succeeded" {
		s.fail(c, conflict)
		return
	}
	if len(q.ResultObject) == 0 || string(q.ResultObject) == "null" {
		s.fail(c, problem(410, "SANDBOX_RESULT_EXPIRED", "查询结果已回收，请重新执行查询"))
		return
	}
	var object FileConfig
	if e = json.Unmarshal(q.ResultObject, &object); e != nil {
		s.fail(c, e)
		return
	}
	rows, e := s.sandboxReadObject(c.Request.Context(), object)
	if e != nil {
		s.fail(c, e)
		return
	}
	sandboxRecords(c, rows)
}
func (s *Server) sandboxQueryCancel(c *gin.Context) {
	var q DataSandboxQuery
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, e := sandboxRead(tx, principal(c), c.Param("id"), true); e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sandbox_id=? AND id=?", c.Param("id"), c.Param("query_id")).Take(&q).Error; e != nil {
			return e
		}
		if q.State != "queued" && q.State != "running" && q.State != "unknown" {
			return conflict
		}
		q.CancelRequested = true
		q.UpdatedAt = time.Now().UTC()
		updates := map[string]interface{}{"cancel_requested": true, "updated_at": q.UpdatedAt}
		if q.State == "queued" {
			q.State = "canceled"
			updates["state"] = "canceled"
			updates["reserved_bytes"] = 0
			updates["lease_owner"] = nil
			updates["lease_until"] = nil
		}
		if e := sandboxReleaseUnstartedResult(tx, q); e != nil {
			return e
		}
		if e := tx.Model(&q).Updates(updates).Error; e != nil {
			return e
		}
		return s.audit(tx, principal(c), "sandbox_cancel", q.ID, "success", c.GetString("trace_id"))
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, q)
}
func (s *Server) sandboxQueryCreate(tx *gorm.DB, p identity.Principal, id, tableID string, body []byte) (int, json.RawMessage, string, error) {

	var in struct {
		SQL       string `json:"sql"`
		Mode      string `json:"mode"`
		TableName string `json:"table_name"`
	}
	if json.Unmarshal(body, &in) != nil || (in.Mode != "preview" && in.Mode != "materialize") {
		return 0, nil, "", invalid
	}
	var query DataSandboxQuery
	err := func() error {
		box, e := sandboxRead(tx, p, id, true)
		if e != nil {
			return e
		}
		tables := []DataSandboxTable{}
		if e = tx.Where("sandbox_id=? AND state='ready'", box.ID).Find(&tables).Error; e != nil {
			return e
		}
		mapping := map[string]sandboxSQLTable{}
		for _, table := range tables {
			var fields []Field
			if e = json.Unmarshal(table.Fields, &fields); e != nil {
				return e
			}
			mapping[table.Name] = sandboxSQLTable{Database: sandboxDatabase, Table: table.PhysicalTable, Fields: fields}
		}
		if _, e = compileSandboxSQL(in.SQL, mapping); e != nil {
			return problem(400, "SANDBOX_SQL_INVALID", "查询包含不支持的语法、函数或未就绪表")
		}
		id, e := newID()
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		query = DataSandboxQuery{ID: id, SandboxID: box.ID, SQL: in.SQL, Mode: in.Mode, State: "queued", Fields: encode([]Field{}), SandboxVersion: &box.Version, CreatedAt: now, UpdatedAt: now}
		if in.Mode == "materialize" {
			if e = sandboxReserve(tx, box, in.TableName, lightweightMaxBytes); e != nil {
				return e
			}
			query.TableNameValue = &in.TableName
		}
		if in.Mode == "preview" {
			var used int64
			if e = tx.Raw(`SELECT COALESCE((SELECT SUM(bytes) FROM data_sandbox_tables WHERE sandbox_id=? AND state<>'removed'),0)+COALESCE((SELECT SUM(reserved_bytes) FROM data_sandbox_queries WHERE sandbox_id=?),0)+COALESCE((SELECT SUM((o.object->>'size')::bigint) FROM data_sandbox_objects o WHERE o.sandbox_id=? AND NOT EXISTS(SELECT 1 FROM data_sandbox_tables t WHERE t.source_object->>'key'=o.key OR t.result_object->>'key'=o.key) AND NOT EXISTS(SELECT 1 FROM data_sandbox_queries q WHERE q.result_object->>'key'=o.key)),0)`, box.ID, box.ID, box.ID).Scan(&used).Error; e != nil {
				return e
			}
			if used+(4<<20) > sandboxMaxBytes {
				return problem(409, "SANDBOX_LIMIT", "沙箱容量已达上限")
			}
			query.ReservedBytes = 4 << 20
		}
		if e = tx.Create(&query).Error; e != nil {
			return e
		}
		if in.Mode == "materialize" {
			table, e := sandboxNewTable(box, in.TableName, []Field{}, map[string]string{}, lightweightMaxBytes)
			if e != nil {
				return e
			}
			table.QueryID = &query.ID
			if e = tx.Create(&table).Error; e != nil {
				return e
			}
			query.ResultTableID = &table.ID
			if e = tx.Save(&query).Error; e != nil {
				return e
			}
		}
		return nil
	}()
	if err != nil {
		return 0, nil, "", err
	}
	return 201, encode(query), query.ID, nil
}

// A queued result with no attempt or object has never reached external submission.
func sandboxReleaseUnstartedResult(tx *gorm.DB, q DataSandboxQuery) error {
	if q.ResultTableID == nil {
		return nil
	}
	return tx.Model(&DataSandboxTable{}).Where("id=? AND sandbox_id=? AND query_id=? AND state='queued' AND attempt_id IS NULL AND source_object IS NULL AND result_object IS NULL", *q.ResultTableID, q.SandboxID, q.ID).Updates(map[string]interface{}{"state": "removed", "bytes": 0, "cancel_requested": true, "lease_owner": nil, "lease_until": nil, "updated_at": time.Now().UTC()}).Error
}
