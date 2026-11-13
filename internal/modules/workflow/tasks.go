package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) createTask(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[TaskInput](b)
	if e != nil {
		return 0, nil, "", e
	}
	source, e := get(tx, "source", v.SourceID, true)
	if e != nil {
		return 0, nil, "", e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: source.Kind, ID: source.ID}, "operate_resource"); err != nil {
		return 0, nil, "", denied
	}
	object, e := get(tx, "object", v.ObjectID, false)
	if e != nil {
		return 0, nil, "", e
	}
	if object.ParentID != source.ID || v.SchemaVersion != object.Version || v.Mode != "cdc" {
		return 0, nil, "", problem(409, "TASK_TEMPLATE_REQUIRED", "当前任务入口绑定已验证的数据库 CDC 模板，模式或结构版本不匹配")
	}
	src, e := decode[SourceSecret](source.Body)
	if e != nil {
		return 0, nil, "", e
	}
	if src.CDCStatus != "passed" {
		return 0, nil, "", problem(409, "PREFLIGHT_REQUIRED", "请先完成来源的 CDC 条件检查")
	}
	if v.DatasetID != nil {
		dataset, err := get(tx, "dataset", *v.DatasetID, false)
		if err != nil {
			return 0, nil, "", err
		}
		d, err := decode[DatasetData](dataset.Body)
		if err != nil {
			return 0, nil, "", err
		}
		if !department(p, dataset) || d.Definition.SourceObjectID != object.ID {
			return 0, nil, "", invalid
		}
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	runID, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	r := Record{Kind: "task", ID: id, DepartmentID: source.DepartmentID, OwnerID: p.UserID, ParentID: source.ID, State: "preflight_passed", Version: 1, Body: encode(TaskData{TaskInput: v, RunID: &runID, Binding: src.Kind})}
	if e = tx.Create(&r).Error; e != nil {
		return 0, nil, "", e
	}
	view, e := s.taskView(tx.Statement.Context, r)
	return 201, view, r.ID, e
}
func (s *Server) taskView(ctx context.Context, r Record) (json.RawMessage, error) {
	v, e := decode[TaskData](r.Body)
	if e != nil {
		return nil, e
	}
	if v.Capture != nil {
		var ctl CDCControl
		if e = s.db.WithContext(ctx).Where("task_id=?", r.ID).Take(&ctl).Error; e != nil {
			return nil, e
		}
		snapshot, err := s.cdcSnapshotBoundary(ctx, r)
		if err != nil {
			return nil, err
		}
		ingestion := progress("pending")
		if r.State == "streaming" {
			ingestion.Status = "running"
		} else if r.State == "manual_required" {
			ingestion.Status = "failed"
		}
		if ctl.LastErrorCode != "" {
			ingestion.ErrorCode = &ctl.LastErrorCode
		}
		archive := progress("pending")
		if v.ArchiveID != "" {
			record, err := get(s.db.WithContext(ctx), "archive", v.ArchiveID, false)
			if err != nil {
				return nil, err
			}
			meta, err := decode[ArchiveData](record.Body)
			if err != nil {
				return nil, err
			}
			if record.State == "verified" {
				archive.Status = "complete"
				archive.LastSuccessAt = &record.CreatedAt
				archive.Position = encode(gin.H{"topic": meta.Topic, "partitions": meta.Boundary.Partitions, "end_offset_exclusive": meta.End, "manifest_id": record.ID})
			}
		}
		return encode(gin.H{"task_id": r.ID, "run_id": v.RunID, "source_id": v.SourceID, "object_id": v.ObjectID, "config_version": r.Version, "state": r.State, "capture_binding": v.Capture, "control": ctl, "snapshot": snapshot, "ingestion": ingestion, "processing": progress("pending"), "result": progress("pending"), "archive": archive}), nil
	}

	state := r.State
	ingestion := progress("unknown")
	processing := progress("pending")
	result := progress("pending")
	archive := progress("pending")
	status, err := s.runtime.connector(ctx, v.Binding)
	if err == nil {
		if running(status) {
			state = "streaming"
			ingestion.Status = "running"
		} else if status.Connector.State == "PAUSED" {
			state = "paused"
			ingestion.Status = "pending"
		} else {
			state = "error"
			ingestion.Status = "failed"
		}
	} else {
		code := "CAPTURE_UNAVAILABLE"
		ingestion.ErrorCode = &code
	}
	if v.Binding == "postgres" {
		if e = s.runtime.compute(ctx); e == nil {
			processing.Status = "running"
			result.Status = "running"
		} else {
			code := "COMPUTE_NOT_READY"
			processing.ErrorCode = &code
		}
	} else {
		code := "RESULT_TEMPLATE_UNAVAILABLE"
		processing.ErrorCode = &code
	}
	if v.ArchiveID != "" {
		a, err := get(s.db.WithContext(ctx), "archive", v.ArchiveID, false)
		if err == nil {
			meta, err := decode[ArchiveData](a.Body)
			if err != nil {
				return nil, err
			}
			archive.Status = "complete"
			archive.LastSuccessAt = &a.CreatedAt
			archive.Position = encode(gin.H{"topic": meta.Topic, "end_offset_exclusive": meta.End, "manifest_id": a.ID})
		}
	}
	return encode(gin.H{"task_id": r.ID, "run_id": v.RunID, "source_id": v.SourceID, "object_id": v.ObjectID, "config_version": r.Version, "state": state, "ingestion": ingestion, "processing": processing, "result": result, "archive": archive}), nil
}
func (s *Server) taskAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		s.mutatePath("task_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			if _, e := bind[struct{}](b); e != nil {
				return 0, nil, id, e
			}
			r, e := get(tx, "task", id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "operate_resource"); err != nil {
				return 0, nil, id, err
			}
			taskData, err := decode[TaskData](r.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if taskData.Capture != nil {
				return s.queueCDCControl(tx, p, r, action)
			}
			if action == "reconcile" {
				data, err := decode[TaskData](r.Body)
				if err != nil {
					return 0, nil, id, err
				}
				actual, err := s.runtime.connector(tx.Statement.Context, data.Binding)
				if err != nil {
					return 0, nil, id, err
				}
				if running(actual) {
					r.State = "streaming"
				} else if actual.Connector.State == "PAUSED" {
					r.State = "paused"
				} else {
					r.State = "manual_required"
				}
				if err = save(tx, &r); err != nil {
					return 0, nil, id, err
				}
				out, err := s.taskView(tx.Statement.Context, r)
				return 200, out, id, err
			}
			if r.State == "initializing" || r.State == "pausing" || r.State == "recovering" {
				return 0, nil, id, conflict
			}
			if action == "pause" {
				r.State = "pausing"
			} else if action == "resume" {
				r.State = "recovering"
			} else {
				r.State = "initializing"
			}
			if e = save(tx, &r); e != nil {
				return 0, nil, id, e
			}
			return 202, encode(gin.H{"action": action}), id, nil
		})(c)
		if c.Writer.Status() == 202 {
			current, err := get(s.db.WithContext(c.Request.Context()), "task", id, false)
			if err != nil {
				return
			}
			data, err := decode[TaskData](current.Body)
			if err != nil || data.Capture != nil {
				return
			}
			var op Operation
			if e := s.db.Where("actor_id=? AND idempotency_key=?", principal(c).UserID, c.GetHeader("Idempotency-Key")).Take(&op).Error; e == nil {
				go s.control(op.ID, id, action)
			}
		}
	}
}
func (s *Server) control(opID, taskID, action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if !s.beginWork(ctx, opID) {
		return
	}
	defer func() { <-s.work }()
	r, e := get(s.db.WithContext(ctx), "task", taskID, false)
	if e != nil {
		s.finish(ctx, opID, e)
		return
	}
	v, e := decode[TaskData](r.Body)
	if e != nil {
		s.finish(ctx, opID, e)
		return
	}
	method := "resume"
	expected := "RUNNING"
	if action == "pause" {
		method = "pause"
		expected = "PAUSED"
	}
	_, e = s.runtime.request(ctx, "PUT", "/connectors/datamesh-"+v.Binding+"/"+method, nil)
	confirmed := false
	if e == nil {
		for range 15 {
			status, err := s.runtime.connector(ctx, v.Binding)
			if err == nil && status.Connector.State == expected && len(status.Tasks) > 0 {
				confirmed = true
				for _, t := range status.Tasks {
					confirmed = confirmed && t.State == expected
				}
				if confirmed {
					break
				}
			}
			select {
			case <-ctx.Done():
				e = ctx.Err()
			case <-time.After(time.Second):
			}
			if e != nil {
				break
			}
		}
	}
	if e == nil && !confirmed {
		e = problem(409, "CONTROL_UNKNOWN", "执行结果尚未确认，请查询实际采集器状态")
	}
	r.State = "error"
	if confirmed {
		r.State = "streaming"
		if action == "pause" {
			r.State = "paused"
		}
	}
	if err := save(s.db.WithContext(ctx), &r); err != nil {
		e = err
	}
	s.finish(context.Background(), opID, e)
}
func (s *Server) claim(ctx context.Context, id string) bool {
	result := s.db.WithContext(ctx).Model(&Operation{}).Where("id=? AND status='accepted'", id).Update("status", "running")
	if result.Error != nil {
		slog.Error("operation claim failed", "operation_id", id)
		return false
	}
	return result.RowsAffected == 1
}
func (s *Server) finish(ctx context.Context, id string, e error) {
	status := "succeeded"
	body := encode(gin.H{"message": "操作完成"})
	if e != nil {
		status = "failed"
		var p *Problem
		if errors.As(e, &p) {
			body = encode(gin.H{"code": p.Code, "message": p.Message})
			if p.Code == "CONTROL_UNKNOWN" {
				status = "unknown"
			}
		} else {
			body = encode(gin.H{"code": "OPERATION_FAILED", "message": "操作未完成，请检查依赖状态"})
			status = "unknown"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.db.WithContext(ctx).Model(&Operation{}).Where("id=?", id).Select("status", "response", "updated_at").Updates(Operation{Status: status, Response: body, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		slog.Error("operation status persistence failed", "operation_id", id)
	}
}

func (s *Server) beginWork(ctx context.Context, id string) bool {
	if !s.claim(ctx, id) {
		return false
	}
	select {
	case s.work <- struct{}{}:
		return true
	default:
		s.finish(context.Background(), id, problem(429, "OPERATION_LIMIT", "同时运行的操作已达上限，请等待后重试"))
		return false
	}
}
