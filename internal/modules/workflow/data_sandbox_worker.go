package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/go-sql-driver/mysql"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The database singleton spans processes. A lease exceeds every bounded engine operation.
const sandboxLease = 4 * time.Minute

func (s *Server) StartDataSandboxWorker(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.sandboxTick(ctx); err != nil && ctx.Err() == nil {
					slog.Error("sandbox worker operation failed")
				}
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
func (s *Server) sandboxActor(ctx context.Context, box DataSandbox) error {
	var owner identity.User
	if e := s.db.WithContext(ctx).Where("id=? AND active=true", box.OwnerID).Take(&owner).Error; e != nil {
		return e
	}
	if owner.Version != box.OwnerVersion || owner.PublicTest != (box.VisitorScope != "") {
		return denied
	}
	p, e := s.etlPrincipal(ctx, box.OwnerID)
	if e != nil {
		return e
	}
	if p.DepartmentID != box.DepartmentID || !role(p, "governor") || !(admin(p) || slices.Contains(p.Platforms, "governance")) {
		return denied
	}
	return nil
}
func (s *Server) sandboxTick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	token, e := newID()
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Exec("UPDATE data_sandbox_worker_lock SET lease_owner=?,lease_until=? WHERE id=1 AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP)", token, now.Add(sandboxLease))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	defer s.db.WithContext(context.WithoutCancel(ctx)).Exec("UPDATE data_sandbox_worker_lock SET lease_owner=NULL,lease_until=NULL WHERE id=1 AND lease_owner=?", token)
	if e = s.sandboxCollectOrphan(ctx); e != nil {
		return e
	}
	if e = s.sandboxExpirePreview(ctx); e != nil {
		return e
	}
	// A lost write lease is never authorization to replay an external submission.
	if e = s.db.WithContext(ctx).Exec("UPDATE data_sandbox_tables SET state='unknown',updated_at=now() WHERE state='running' AND lease_until<CURRENT_TIMESTAMP").Error; e != nil {
		return e
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE data_sandbox_queries SET state=CASE WHEN cancel_requested AND mode='preview' THEN 'canceled' WHEN mode='materialize' THEN 'unknown' ELSE 'failed' END,error_code='SANDBOX_INTERRUPTED',reserved_bytes=0,updated_at=now() WHERE state='running' AND lease_until<CURRENT_TIMESTAMP").Error; e != nil {
		return e
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE data_sandboxes SET state='closing',version=version+1,updated_at=now() WHERE state='active' AND expires_at<CURRENT_TIMESTAMP").Error; e != nil {
		return e
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE data_sandbox_queries q SET state='failed',error_code='SANDBOX_INTERRUPTED' WHERE q.state='unknown' AND EXISTS (SELECT 1 FROM data_sandbox_tables t WHERE t.id=q.result_table_id AND t.state IN ('queued','failed'))").Error; e != nil {
		return e
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE data_sandbox_tables t SET state='removed',bytes=0,lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE t.state='queued' AND t.attempt_id IS NULL AND t.source_object IS NULL AND t.result_object IS NULL AND EXISTS(SELECT 1 FROM data_sandbox_queries q WHERE q.id=t.query_id AND q.sandbox_id=t.sandbox_id AND q.state IN ('failed','canceled'))").Error; e != nil {
		return e
	}
	var box DataSandbox
	if e = s.db.WithContext(ctx).Where("state='closing'").Order("created_at,id").Take(&box).Error; e == nil {
		return s.sandboxCleanupBox(ctx, box)
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	var table DataSandboxTable
	if e = s.db.WithContext(ctx).Where("state IN ?", []string{"removing", "unknown"}).Order("created_at,id").Take(&table).Error; e == nil {
		if table.State == "removing" {
			return s.sandboxCleanupTable(ctx, table)
		}
		return s.sandboxReconcile(ctx, table, token)
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	var query DataSandboxQuery
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='queued'").Order("created_at,id").Take(&query).Error; e != nil {
			return e
		}
		if e := tx.Where("id=?", query.SandboxID).Take(&box).Error; e != nil {
			return e
		}
		query.State = "running"
		query.LeaseOwner = &token
		until := now.Add(sandboxLease)
		query.LeaseUntil = &until
		query.AttemptID = &token
		return tx.Save(&query).Error
	})
	if e == nil {
		return s.sandboxExecuteQuery(ctx, box, query, token)
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='queued' AND query_id IS NULL").Order("created_at,id").Take(&table).Error; e != nil {
			return e
		}
		if e := tx.Where("id=?", table.SandboxID).Take(&box).Error; e != nil {
			return e
		}
		table.State = "running"
		table.LeaseOwner = &token
		until := now.Add(sandboxLease)
		table.LeaseUntil = &until
		table.AttemptID = &token
		label := "sandbox_" + table.ID
		table.LoadLabel = &label
		return tx.Save(&table).Error
	})
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	return s.sandboxExecuteImport(ctx, box, table, token)
}
func sandboxBoxValid(box DataSandbox, version *int) bool {
	return version != nil && *version == box.Version && box.State == "active" && box.ExpiresAt.After(time.Now())
}
func (s *Server) sandboxFence(ctx context.Context, tx *gorm.DB, boxID, token string, version *int) error {
	var box DataSandbox
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", boxID).Take(&box).Error; e != nil {
		return e
	}
	if !sandboxBoxValid(box, version) {
		return conflict
	}
	var n int64
	if e := tx.Table("data_sandbox_worker_lock").Where("id=1 AND lease_owner=? AND lease_until>CURRENT_TIMESTAMP", token).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return conflict
	}
	return s.sandboxActor(ctx, box)
}
func (s *Server) sandboxTableFail(ctx context.Context, t DataSandboxTable, token, state string) error {
	code := "SANDBOX_EXECUTION_FAILED"
	if t.ErrorCode != nil {
		code = *t.ErrorCode
	}
	updates := map[string]interface{}{"state": state, "error_code": code, "updated_at": time.Now().UTC()}
	if state != "unknown" {
		updates["lease_owner"] = nil
		updates["lease_until"] = nil
		if t.QueryID != nil {
			updates["state"] = "removing"
		}
	}
	return s.db.WithContext(ctx).Model(&DataSandboxTable{}).Where("id=? AND lease_owner=? AND state IN ?", t.ID, token, []string{"running", "unknown"}).Updates(updates).Error
}
func (s *Server) sandboxExecuteImport(ctx context.Context, box DataSandbox, t DataSandboxTable, token string) error {
	if !sandboxBoxValid(box, t.SandboxVersion) || t.CancelRequested {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	if e := s.sandboxActor(ctx, box); e != nil {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	var rows sandboxRows
	var object FileConfig
	var e error
	if len(t.ResultObject) > 0 && string(t.ResultObject) != "null" {
		e = json.Unmarshal(t.ResultObject, &object)
		if e == nil {
			rows, e = s.sandboxReadObject(ctx, object)
		}
	} else {
		e = json.Unmarshal(t.SourceObject, &object)
		if e == nil {
			var data []byte
			data, e = s.readStoredFile(ctx, object, lightweightMaxBytes)
			if e == nil {
				var file lightweightFileDTO
				var values [][]string
				file, values, e = parseLightweightCSV(object.Name, data)
				data = nil
				rows = sandboxRows{Fields: file.Fields, FieldLabels: file.FieldLabels, Values: make([][]interface{}, len(values))}
				for index, record := range values {
					// Release the parsed matrix progressively while building typed rows.
					values[index] = nil
					row := make([]interface{}, len(record))
					for i, v := range record {
						row[i] = v
					}
					rows.Values[index] = row
				}
				values = nil
			}
		}
	}
	if e != nil {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	return s.sandboxWritePublish(ctx, box, t, rows, token)
}
func (s *Server) sandboxWritePublish(ctx context.Context, box DataSandbox, t DataSandboxTable, rows sandboxRows, token string) error {
	data, e := sandboxEncodeObject(rows)
	if e != nil {
		var issue *Problem
		if errors.As(e, &issue) {
			t.ErrorCode = &issue.Code
		}
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	var object FileConfig
	object.Size = int64(len(data))
	// Freeze exact canonical bytes and identity before any engine write.
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.sandboxFence(ctx, tx, box.ID, token, t.SandboxVersion); e != nil {
			return e
		}
		if t.QueryID != nil {
			var q DataSandboxQuery
			if e := tx.Where("id=?", *t.QueryID).Take(&q).Error; e != nil {
				return e
			}
			if q.CancelRequested {
				return conflict
			}
		}
		var used int64
		if e := tx.Model(&DataSandboxTable{}).Where("sandbox_id=? AND id<>? AND state<>'removed'", box.ID, t.ID).Select("COALESCE(SUM(bytes),0)").Scan(&used).Error; e != nil {
			return e
		}
		size := object.Size
		var source FileConfig
		if len(t.SourceObject) > 0 && string(t.SourceObject) != "null" {
			if e := json.Unmarshal(t.SourceObject, &source); e != nil {
				return e
			}
			size += source.Size
		}
		var previews int64
		if e := tx.Model(&DataSandboxQuery{}).Where("sandbox_id=?", box.ID).Select("COALESCE(SUM(reserved_bytes),0)").Scan(&previews).Error; e != nil {
			return e
		}
		if used+previews+size > sandboxMaxBytes {
			return invalid
		}
		result := tx.Model(&DataSandboxTable{}).Where("id=? AND lease_owner=? AND cancel_requested=false AND state='running'", t.ID, token).Updates(map[string]interface{}{"bytes": size})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict
		}
		return nil
	})
	if e != nil {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	if len(t.ResultObject) > 0 && string(t.ResultObject) != "null" {
		e = json.Unmarshal(t.ResultObject, &object)
	} else {
		object, e = s.sandboxStoreFile(sandboxObjectContext(ctx, box.ID), "sandbox-result.json", "application/json", data)
	}
	if e != nil {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.sandboxFence(ctx, tx, box.ID, token, t.SandboxVersion); e != nil {
			return e
		}
		if t.QueryID != nil {
			var q DataSandboxQuery
			if e := tx.Where("id=?", *t.QueryID).Take(&q).Error; e != nil {
				return e
			}
			if q.CancelRequested {
				return conflict
			}
		}
		result := tx.Model(&DataSandboxTable{}).Where("id=? AND lease_owner=? AND cancel_requested=false AND state='running'", t.ID, token).Updates(map[string]interface{}{"result_object": encode(object), "source_digest": object.SHA256, "fields": encode(rows.Fields), "field_labels": encode(rows.FieldLabels), "rows": len(rows.Values)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict
		}
		return nil
	})
	if e != nil {
		return s.sandboxTableFail(ctx, t, token, "failed")
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	e = sandboxDorisWrite(work, t.PhysicalTable, rows.Fields, rows.Values, *t.LoadLabel)
	cancel()
	if e != nil {
		return s.sandboxTableFail(ctx, t, token, "unknown")
	}
	return s.sandboxPublish(ctx, t, token)
}
func (s *Server) sandboxPublish(ctx context.Context, t DataSandboxTable, token string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.sandboxFence(ctx, tx, t.SandboxID, token, t.SandboxVersion); e != nil {
			return e
		}
		result := tx.Model(&DataSandboxTable{}).Where("id=? AND lease_owner=? AND cancel_requested=false AND lease_until>CURRENT_TIMESTAMP AND state IN ?", t.ID, token, []string{"running", "unknown"}).Updates(map[string]interface{}{"state": "ready", "lease_owner": nil, "lease_until": nil, "error_code": nil, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict
		}
		if t.QueryID != nil {
			var current DataSandboxTable
			if e := tx.Where("id=?", t.ID).Take(&current).Error; e != nil {
				return e
			}
			result = tx.Model(&DataSandboxQuery{}).Where("id=? AND cancel_requested=false", *t.QueryID).Updates(map[string]interface{}{"state": "succeeded", "lease_owner": nil, "lease_until": nil, "fields": current.Fields, "row_count": current.Rows, "result_object": current.ResultObject, "error_code": nil, "updated_at": time.Now().UTC()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return conflict
			}
		}
		return nil
	})
}
func (s *Server) sandboxQueryFail(ctx context.Context, q DataSandboxQuery, token, state string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{"state": gorm.Expr("CASE WHEN cancel_requested AND ?<>'unknown' THEN 'canceled' ELSE ? END", state, state), "reserved_bytes": 0, "error_code": "SANDBOX_EXECUTION_FAILED", "updated_at": time.Now().UTC()}
		if state != "unknown" {
			updates["lease_owner"] = nil
			updates["lease_until"] = nil
		}
		if e := tx.Model(&DataSandboxQuery{}).Where("id=? AND lease_owner=?", q.ID, token).Updates(updates).Error; e != nil {
			return e
		}
		if q.ResultTableID != nil && state != "unknown" {
			return sandboxReleaseUnstartedResult(tx, q)
		}
		return nil
	})
}
func (s *Server) sandboxExecuteQuery(ctx context.Context, box DataSandbox, q DataSandboxQuery, token string) error {
	if !sandboxBoxValid(box, q.SandboxVersion) || q.CancelRequested {
		return s.sandboxQueryFail(ctx, q, token, "canceled")
	}
	if e := s.sandboxActor(ctx, box); e != nil {
		return s.sandboxQueryFail(ctx, q, token, "failed")
	}
	tables := []DataSandboxTable{}
	if e := s.db.WithContext(ctx).Where("sandbox_id=? AND state='ready'", box.ID).Find(&tables).Error; e != nil {
		return e
	}
	mapping := map[string]sandboxSQLTable{}
	for _, t := range tables {
		var fields []Field
		if e := json.Unmarshal(t.Fields, &fields); e != nil {
			return e
		}
		mapping[t.Name] = sandboxSQLTable{Database: sandboxDatabase, Table: t.PhysicalTable, Fields: fields}
	}
	if e := s.db.WithContext(ctx).Model(&DataSandboxQuery{}).Where("id=? AND lease_owner=?", q.ID, token).Update("input_tables", encode(mapping)).Error; e != nil {
		return e
	}
	sql, e := compileSandboxSQL(q.SQL, mapping)
	if e != nil {
		return s.sandboxQueryFail(ctx, q, token, "failed")
	}
	limit, size := 1000, 4<<20
	if q.Mode == "materialize" {
		limit, size = lightweightMaxRows, sandboxCanonicalMaxBytes
	}
	work, cancel := context.WithTimeout(ctx, 25*time.Second)
	rows, e := sandboxDorisRead(work, sql, limit, size)
	cancel()
	if e != nil {
		return s.sandboxQueryFail(ctx, q, token, "failed")
	}
	if q.Mode == "materialize" {
		if _, e = sandboxCSVSize(rows); e != nil {
			return s.sandboxQueryFail(ctx, q, token, "failed")
		}
		if rows.Truncated || q.ResultTableID == nil {
			return s.sandboxQueryFail(ctx, q, token, "failed")
		}
		var t DataSandboxTable
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := s.sandboxFence(ctx, tx, box.ID, token, q.SandboxVersion); e != nil {
				return e
			}
			var latest DataSandboxQuery
			if e := tx.Where("id=?", q.ID).Take(&latest).Error; e != nil {
				return e
			}
			if latest.CancelRequested {
				return conflict
			}
			if e := tx.Where("id=? AND query_id=? AND state='queued' AND attempt_id IS NULL", *q.ResultTableID, q.ID).Take(&t).Error; e != nil {
				return e
			}
			t.State = "running"
			t.LeaseOwner = &token
			t.LeaseUntil = q.LeaseUntil
			t.AttemptID = &token
			label := "sandbox_" + t.ID
			t.LoadLabel = &label
			return tx.Save(&t).Error
		})
		if e != nil {
			return s.sandboxQueryFail(ctx, q, token, "failed")
		}
		e = s.sandboxWritePublish(ctx, box, t, rows, token)
		if e != nil {
			return e
		}
		if e = s.db.WithContext(ctx).Where("id=?", t.ID).Take(&t).Error; e != nil {
			return e
		}
		if t.State != "ready" {
			state := "failed"
			if t.State == "unknown" {
				state = "unknown"
			}
			return s.sandboxQueryFail(ctx, q, token, state)
		}
		return nil
	}
	object, e := s.sandboxStoreObject(sandboxObjectContext(ctx, box.ID), rows)
	if e != nil {
		return s.sandboxQueryFail(ctx, q, token, "failed")
	}
	if object.Size > 4<<20 {
		return s.sandboxQueryFail(ctx, q, token, "failed")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.sandboxFence(ctx, tx, box.ID, token, q.SandboxVersion); e != nil {
			return e
		}
		result := tx.Model(&DataSandboxQuery{}).Where("id=? AND lease_owner=? AND state='running' AND cancel_requested=false AND lease_until>CURRENT_TIMESTAMP", q.ID, token).Updates(map[string]interface{}{"state": "succeeded", "lease_owner": nil, "lease_until": nil, "fields": encode(rows.Fields), "row_count": len(rows.Values), "truncated": rows.Truncated, "reserved_bytes": object.Size, "result_object": encode(object), "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict
		}
		return nil
	})
}
func (s *Server) sandboxReconcile(ctx context.Context, t DataSandboxTable, token string) error {
	if t.LeaseUntil != nil && t.LeaseUntil.After(time.Now()) {
		return nil
	}
	var box DataSandbox
	if e := s.db.WithContext(ctx).Where("id=?", t.SandboxID).Take(&box).Error; e != nil {
		return e
	}
	if !sandboxBoxValid(box, t.SandboxVersion) || t.CancelRequested {
		return s.sandboxCleanupTable(ctx, t)
	}
	if t.QueryID != nil {
		var q DataSandboxQuery
		if e := s.db.WithContext(ctx).Where("id=?", *t.QueryID).Take(&q).Error; e != nil {
			return e
		}
		if q.CancelRequested {
			return s.sandboxCleanupTable(ctx, t)
		}
	}
	if e := s.sandboxActor(ctx, box); e != nil {
		return s.sandboxCleanupTable(ctx, t)
	}
	if len(t.ResultObject) == 0 || string(t.ResultObject) == "null" {
		return s.sandboxCleanupTable(ctx, t)
	}
	var object FileConfig
	if e := json.Unmarshal(t.ResultObject, &object); e != nil {
		return e
	}
	if t.SourceDigest == nil || *t.SourceDigest != object.SHA256 {
		return invalid
	}
	rows, e := s.sandboxReadObject(ctx, object)
	if e != nil {
		return e
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	verified, e := sandboxDorisVerify(work, t.PhysicalTable, rows.Fields, rows.Values)
	cancel()
	if e != nil {
		var missing *mysql.MySQLError
		if !errors.As(e, &missing) || missing.Number != 1146 {
			return e
		}
		verified = false
	}
	if !verified {
		return s.sandboxCleanupTable(ctx, t)
	}
	until := time.Now().UTC().Add(sandboxLease)
	t.LeaseOwner = &token
	t.LeaseUntil = &until
	if e = s.db.WithContext(ctx).Model(&t).Updates(map[string]interface{}{"lease_owner": token, "lease_until": until}).Error; e != nil {
		return e
	}
	return s.sandboxPublish(ctx, t, token)
}
func (s *Server) sandboxDeleteObject(ctx context.Context, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var object FileConfig
	if e := json.Unmarshal(raw, &object); e != nil {
		return e
	}
	if object.Key == "" {
		return invalid
	}
	var tracked int64
	if e := s.db.WithContext(ctx).Table("data_sandbox_objects").Where("key=?", object.Key).Count(&tracked).Error; e != nil {
		return e
	}
	if tracked == 0 {
		return nil
	}
	if e := s.runtime.store.RemoveObject(ctx, resourceBucket, object.Key, minio.RemoveObjectOptions{}); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Exec("DELETE FROM data_sandbox_objects WHERE key=?", object.Key).Error
}
func (s *Server) sandboxCleanupTable(ctx context.Context, t DataSandboxTable) error {
	if t.LeaseUntil != nil && t.LeaseUntil.After(time.Now()) {
		return nil
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	e := sandboxDorisDrop(work, t.PhysicalTable)
	cancel()
	if e != nil {
		return e
	}
	if e = s.sandboxDeleteObject(ctx, t.SourceObject); e != nil {
		return e
	}
	if e = s.sandboxDeleteObject(ctx, t.ResultObject); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&DataSandboxTable{}).Where("id=? AND state<>'removed'", t.ID).Updates(map[string]interface{}{"state": "removed", "source_object": nil, "result_object": nil, "bytes": 0, "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
		if t.QueryID != nil {
			return tx.Model(&DataSandboxQuery{}).Where("id=? AND state IN ?", *t.QueryID, []string{"queued", "running", "unknown"}).Updates(map[string]interface{}{"state": gorm.Expr("CASE WHEN cancel_requested THEN 'canceled' ELSE 'failed' END"), "error_code": "SANDBOX_NOT_PUBLISHED", "updated_at": time.Now().UTC()}).Error
		}
		return nil
	})
}
func (s *Server) sandboxCleanupBox(ctx context.Context, box DataSandbox) error {
	tables := []DataSandboxTable{}
	if e := s.db.WithContext(ctx).Where("sandbox_id=? AND state<>'removed'", box.ID).Find(&tables).Error; e != nil {
		return e
	}
	for _, t := range tables {
		if t.LeaseUntil != nil && t.LeaseUntil.After(time.Now()) {
			return nil
		}
		if e := s.sandboxCleanupTable(ctx, t); e != nil {
			return e
		}
		return nil
	}
	queries := []DataSandboxQuery{}
	if e := s.db.WithContext(ctx).Where("sandbox_id=?", box.ID).Find(&queries).Error; e != nil {
		return e
	}
	for _, q := range queries {
		if q.LeaseUntil != nil && q.LeaseUntil.After(time.Now()) && q.State == "running" {
			return nil
		}
		if e := s.sandboxDeleteObject(ctx, q.ResultObject); e != nil {
			return e
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&DataSandboxQuery{}).Where("sandbox_id=?", box.ID).Updates(map[string]interface{}{"result_object": nil, "reserved_bytes": 0, "cancel_requested": true}).Error; e != nil {
			return e
		}
		return tx.Model(&DataSandbox{}).Where("id=? AND state='closing'", box.ID).Updates(map[string]interface{}{"state": "closed", "updated_at": time.Now().UTC()}).Error
	})
}

func (s *Server) sandboxCollectOrphan(ctx context.Context) error {
	var pending struct {
		Key    string
		Object json.RawMessage
	}
	err := s.db.WithContext(ctx).Raw(`SELECT o.key,o.object FROM data_sandbox_objects o WHERE o.created_at<now()-interval '10 minutes'
 AND NOT EXISTS(SELECT 1 FROM data_sandbox_tables t WHERE t.source_object->>'key'=o.key OR t.result_object->>'key'=o.key)
 AND NOT EXISTS(SELECT 1 FROM data_sandbox_queries q WHERE q.result_object->>'key'=o.key)
 ORDER BY o.created_at LIMIT 1`).Scan(&pending).Error
	if err != nil || pending.Key == "" {
		return err
	}
	return s.sandboxDeleteObject(ctx, pending.Object)
}
func (s *Server) sandboxExpirePreview(ctx context.Context) error {
	var q DataSandboxQuery
	e := s.db.WithContext(ctx).Where("mode='preview' AND state='succeeded' AND reserved_bytes>0 AND updated_at<now()-interval '24 hours'").Order("created_at,id").Take(&q).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = s.sandboxDeleteObject(ctx, q.ResultObject); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Model(&q).Updates(map[string]interface{}{"result_object": nil, "reserved_bytes": 0, "error_code": "SANDBOX_RESULT_EXPIRED"}).Error
}
