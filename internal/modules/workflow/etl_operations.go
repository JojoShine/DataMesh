package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ETLControl struct {
	RunID           string `gorm:"primaryKey"`
	CancelRequested bool
	Active          bool
	HeartbeatAt     time.Time
	LeaseOwner      string
	LeaseExpiresAt  time.Time
	TargetKey       string
	StageStartedAt  time.Time
}

func (ETLControl) TableName() string { return "etl_run_controls" }

type ETLEvent struct {
	ID        int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	RunID     string    `json:"run_id"`
	TaskID    string    `json:"task_id"`
	Level     string    `json:"level"`
	Stage     string    `json:"stage"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	ActorID   string    `json:"actor_id"`
	TraceID   string    `json:"trace_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (ETLEvent) TableName() string { return "etl_events" }

type ETLAlert struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Code      string    `json:"code"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	State     string    `json:"state"`
	HandledBy string    `json:"handled_by"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ETLAlert) TableName() string { return "etl_alerts" }

type ETLResolution struct {
	Outcome  string    `json:"outcome"`
	Reason   string    `json:"reason"`
	Evidence string    `json:"evidence"`
	ActorID  string    `json:"actor_id"`
	At       time.Time `json:"at"`
}
type etlTaskWrite struct {
	ETLDefinition
	ExpectedVersion int  `json:"expected_version"`
	Draft           bool `json:"draft"`
}

func etlTaskTransition(state, action string) (string, error) {
	switch action {
	case "enable":
		if state == "draft" || state == "disabled" {
			return "ready", nil
		}
	case "disable":
		if state == "ready" {
			return "disabled", nil
		}
	case "archive":
		if state == "disabled" || state == "draft" {
			return "archived", nil
		}
	}
	return "", conflict
}
func etlTerminal(state string) bool {
	return slices.Contains([]string{"succeeded", "failed", "cancelled"}, state)
}
func etlOwner(tx *gorm.DB, p identity.Principal, kind, id string, lock bool) (Record, error) {
	r, e := get(tx, kind, id, lock)
	if e == nil && !admin(p) && r.OwnerID != p.UserID {
		e = denied
	}
	return r, e
}
func etlPending(tx *gorm.DB, id string) error {
	var count int64
	if e := tx.Model(&Record{}).Where("kind='etl_run' AND parent_id=? AND state IN ?", id, []string{"queued", "extracting", "loading", "committing", "unknown"}).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return problem(409, "ETL_RUN_PENDING", "存在未结束或待核实运行，请先处理")
	}
	return nil
}
func etlEvent(tx *gorm.DB, row Record, p identity.Principal, level, code, message, trace string) error {
	return tx.Create(&ETLEvent{RunID: row.ID, TaskID: row.ParentID, Level: level, Stage: row.State, Code: code, Message: message, ActorID: p.UserID, TraceID: trace}).Error
}
func etlAlert(tx *gorm.DB, row Record, code, severity, message string) error {
	id, e := newID()
	if e != nil {
		return e
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "code"}}, DoNothing: true}).Create(&ETLAlert{ID: id, RunID: row.ID, Code: code, Severity: severity, Message: message, State: "open"}).Error
}
func (s *Server) enqueueETL(tx *gorm.DB, p identity.Principal, task Record, d ETLDefinition, retryOf string, attempt int, trace string) (Record, error) {
	if task.State != "ready" {
		return Record{}, problem(409, "ETL_TASK_DISABLED", "任务须启用后才能执行")
	}
	if e := s.etlPermission(tx, p, d); e != nil {
		return Record{}, e
	}
	if e := etlPending(tx, task.ID); e != nil {
		return Record{}, e
	}
	id, e := newID()
	if e != nil {
		return Record{}, e
	}
	run := ETLRun{Definition: d, Batches: []ETLBatch{}, Message: "等待执行", TaskVersion: task.Version, Attempt: attempt, RetryOf: retryOf, TraceID: trace}
	r := Record{Kind: "etl_run", ID: id, ParentID: task.ID, DepartmentID: task.DepartmentID, OwnerID: p.UserID, State: "queued", Version: 1, Body: encode(run)}
	if e = tx.Create(&r).Error; e != nil {
		return r, e
	}
	key, e := etlTargetKey(tx, d)
	if e != nil {
		return r, e
	}
	if e = tx.Create(&ETLControl{RunID: id, TargetKey: key}).Error; e != nil {
		return r, e
	}
	reportID, e := newID()
	if e != nil {
		return r, e
	}
	if e = tx.Create(&ETLReconciliation{ID: reportID, RunID: id, Version: 1, State: "pending", TaskVersion: task.Version, InputScope: json.RawMessage("null")}).Error; e != nil {
		return r, e
	}
	return r, etlEvent(tx, r, p, "info", "RUN_QUEUED", "运行已创建；执行时重新读取当前来源", trace)
}
func (s *Server) registerETLOperations(api, write *gin.RouterGroup) {
	s.registerETLReconciliation(api, write)
	s.registerDeliveryReceipts(api, write)
	api.GET("/etl-runs/:id/events", s.require("exchange", "operator"), s.etlEvents)
	api.GET("/etl-alerts", s.require("exchange", "operator"), s.etlAlerts)
	for _, a := range []string{"enable", "disable", "archive", "copy", "revisions"} {
		action := a
		write.POST("/etl-tasks/:id/"+action, s.require("exchange", "operator"), s.mutatePath("etl_task_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			r, e := etlOwner(tx, p, "etl_task", id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if action == "revisions" {
				v, err := bind[etlTaskWrite](b)
				if err != nil {
					return 0, nil, id, err
				}
				if v.ExpectedVersion != r.Version || r.State == "archived" {
					return 0, nil, id, conflict
				}
				if e = etlPending(tx, id); e != nil {
					return 0, nil, id, e
				}
				d, _, _, from, to, err := s.prepareETL(tx.Statement.Context, tx, p, v.ETLDefinition)
				if err != nil {
					return 0, nil, id, err
				}
				if from != nil {
					from.Close()
				}
				to.Close()
				r.Body = encode(d)
				r.State = "draft"
				r.Version++
			} else {
				v, err := bind[struct {
					ExpectedVersion int `json:"expected_version"`
				}](b)
				if err != nil {
					return 0, nil, id, err
				}
				if v.ExpectedVersion != r.Version {
					return 0, nil, id, conflict
				}
				if action == "copy" {
					newID, err := newID()
					if err != nil {
						return 0, nil, id, err
					}
					d, err := decode[ETLDefinition](r.Body)
					if err != nil {
						return 0, nil, id, err
					}
					if len([]rune(d.Name)) > 90 {
						d.Name = string([]rune(d.Name)[:90])
					}
					d.Name += "（副本）"
					r.Body = encode(d)
					r.ID = newID
					r.State = "draft"
					r.Version = 1
					r.CreatedAt = time.Time{}
					r.UpdatedAt = time.Time{}
					r.OwnerID = p.UserID
					if e = tx.Create(&r).Error; e != nil {
						return 0, nil, id, e
					}
					return 201, etlView(r), r.ID, nil
				}
				if e = etlPending(tx, id); e != nil {
					return 0, nil, id, e
				}
				state, err := etlTaskTransition(r.State, action)
				if err != nil {
					return 0, nil, id, err
				}
				if action == "enable" {
					d, err := decode[ETLDefinition](r.Body)
					if err != nil {
						return 0, nil, id, err
					}
					_, _, _, from, to, err := s.prepareETL(tx.Statement.Context, tx, p, d)
					if err != nil {
						return 0, nil, id, err
					}
					if from != nil {
						from.Close()
					}
					to.Close()
				}
				r.State = state
				r.Version++
			}
			e = save(tx, &r)
			return 200, etlView(r), id, e
		}))
	}
	for _, a := range []string{"cancel", "retry", "resolve", "reconcile"} {
		action := a
		write.POST("/etl-runs/:id/"+action, s.require("exchange", "operator"), func(c *gin.Context) {
			s.mutatePath("etl_run_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
				// Lock task before run, consistently with submissions and configuration edits.
				initial, e := etlOwner(tx, p, "etl_run", id, false)
				if e != nil {
					return 0, nil, id, e
				}
				task, e := get(tx, "etl_task", initial.ParentID, true)
				if e != nil {
					return 0, nil, id, e
				}
				r, e := etlOwner(tx, p, "etl_run", id, true)
				if e != nil {
					return 0, nil, id, e
				}
				v, e := bind[struct {
					Reason   string `json:"reason"`
					Outcome  string `json:"outcome"`
					Evidence string `json:"evidence"`
				}](b)
				if e != nil || !valid(v.Reason, 1000) {
					return 0, nil, id, invalid
				}
				run, e := decode[ETLRun](r.Body)
				if e != nil {
					return 0, nil, id, e
				}
				if action == "retry" {
					if !slices.Contains([]string{"failed", "cancelled"}, r.State) {
						return 0, nil, id, conflict
					}
					if run.TaskVersion != 0 && run.TaskVersion != task.Version {
						return 0, nil, id, problem(409, "ETL_VERSION_CHANGED", "任务版本已变化，请使用当前任务创建新运行")
					}
					next, e := s.enqueueETL(tx, p, task, run.Definition, id, max(run.Attempt, 1)+1, c.GetString("trace_id"))
					if e != nil {
						return 0, nil, id, e
					}
					if e = etlEvent(tx, next, p, "warning", "RETRY_REQUESTED", "重试原因："+v.Reason, c.GetString("trace_id")); e != nil {
						return 0, nil, id, e
					}
					return 201, etlView(next), next.ID, nil
				}
				if action == "reconcile" {
					if !slices.Contains([]string{"extracting", "loading", "committing"}, r.State) {
						return 0, nil, id, conflict
					}
					var ctl ETLControl
					if e = tx.Where("run_id=?", id).Take(&ctl).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
						return 0, nil, id, e
					}
					if ctl.Active {
						return 0, nil, id, problem(409, "ETL_EXECUTOR_ACTIVE", "执行器仍在运行，请等待结束后核查")
					}
					r.State = "unknown"
					now := time.Now().UTC()
					run.FinishedAt = &now
					run.ErrorCode = "EXECUTOR_INACTIVE"
					run.Message = "执行器已退出，运行结果未完整保存；须核实目标事务"
					r.Body = encode(run)
					if e = save(tx, &r); e != nil {
						return 0, nil, id, e
					}
					if e = etlEvent(tx, r, p, "error", "EXECUTOR_INACTIVE", run.Message+"；核查原因："+v.Reason, c.GetString("trace_id")); e != nil {
						return 0, nil, id, e
					}
					if e = etlAlert(tx, r, "EXECUTOR_INACTIVE", "error", run.Message); e != nil {
						return 0, nil, id, e
					}
					return 200, etlView(r), id, nil
				}
				if action == "resolve" {
					if !admin(p) {
						return 0, nil, id, denied
					}
					if r.State != "unknown" || !slices.Contains([]string{"succeeded", "failed"}, v.Outcome) || !valid(v.Evidence, 2000) {
						return 0, nil, id, invalid
					}
					var ctl ETLControl
					e = tx.Where("run_id=?", id).Take(&ctl).Error
					if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
						return 0, nil, id, e
					}
					if ctl.Active {
						return 0, nil, id, problem(409, "ETL_EXECUTOR_ACTIVE", "执行器尚未结束，暂不能人工修改结果")
					}
					release, e := s.etlCheckTargetIdle(tx.Statement.Context, tx, p, run, ctl)
					if e != nil {
						return 0, nil, id, e
					}
					defer release()
					r.State = v.Outcome
					now := time.Now().UTC()
					run.FinishedAt = &now
					run.Resolution = &ETLResolution{Outcome: v.Outcome, Reason: v.Reason, Evidence: v.Evidence, ActorID: p.UserID, At: now}
					run.Message = "管理员已核实目标事务结果"
					if v.Outcome == "succeeded" {
						run.Written = run.Read
						for i := range run.Batches {
							run.Batches[i].State = "committed"
						}
					} else {
						run.Written = 0
						for i := range run.Batches {
							run.Batches[i].State = "rolled_back"
						}
					}
					r.Body = encode(run)
					if e = save(tx, &r); e != nil {
						return 0, nil, id, e
					}
					if e = tx.Where("run_id=?", id).Delete(&etlTargetLock{}).Error; e != nil {
						return 0, nil, id, e
					}
					if e = etlEvent(tx, r, p, "warning", "RUN_RESOLVED", run.Message, c.GetString("trace_id")); e != nil {
						return 0, nil, id, e
					}
					return 200, etlView(r), id, nil
				}
				if !slices.Contains([]string{"queued", "extracting", "loading"}, r.State) {
					return 0, nil, id, problem(409, "ETL_CANCEL_UNSAFE", "此阶段无法取消；提交中或结果未知的运行须核实事务结果")
				}
				now := time.Now().UTC()
				ctl := ETLControl{RunID: id, CancelRequested: true, HeartbeatAt: now, StageStartedAt: now}
				if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}}, DoUpdates: clause.AssignmentColumns([]string{"cancel_requested"})}).Create(&ctl).Error; e != nil {
					return 0, nil, id, e
				}
				if r.State == "queued" {
					r.State = "cancelled"
					run.FinishedAt = &now
					run.Message = "运行在执行前取消，目标未写入"
					r.Body = encode(run)
					if e = save(tx, &r); e != nil {
						return 0, nil, id, e
					}
				}
				if e = etlEvent(tx, r, p, "warning", "CANCEL_REQUESTED", "已请求取消，提交前终止并回滚；原因："+v.Reason, c.GetString("trace_id")); e != nil {
					return 0, nil, id, e
				}
				return 200, etlView(r), id, nil
			})(c)
		})
	}
	for _, a := range []string{"acknowledge", "resolve"} {
		action := a
		write.POST("/etl-alerts/:id/"+action, s.require("exchange", "operator"), s.mutatePath("etl_alert_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			var alert ETLAlert
			if e := tx.Where("id=?", id).Take(&alert).Error; e != nil {
				return 0, nil, id, e
			}
			run, e := etlOwner(tx, p, "etl_run", alert.RunID, false)
			if e != nil {
				return 0, nil, id, e
			}
			v, e := bind[struct {
				Reason string `json:"reason"`
			}](b)
			if e != nil || !valid(v.Reason, 1000) {
				return 0, nil, id, invalid
			}
			if alert.State == "resolved" {
				return 0, nil, id, conflict
			}
			if action == "acknowledge" && alert.State != "open" {
				return 0, nil, id, conflict
			}
			state := "acknowledged"
			if action == "resolve" {
				state = "resolved"
			}
			result := tx.Model(&ETLAlert{}).Where("id=? AND state=?", id, alert.State).Updates(map[string]interface{}{"state": state, "handled_by": p.UserID, "reason": v.Reason, "updated_at": time.Now().UTC()})
			if result.Error != nil {
				return 0, nil, id, result.Error
			}
			if result.RowsAffected != 1 {
				return 0, nil, id, conflict
			}
			alert.State = state
			alert.Reason = v.Reason
			alert.HandledBy = p.UserID
			if e = etlEvent(tx, run, p, "info", "ALERT_"+strings.ToUpper(state), "告警 "+alert.ID+"；处理说明："+v.Reason, ""); e != nil {
				return 0, nil, id, e
			}
			return 200, encode(alert), id, nil
		}))
	}
}
func (s *Server) etlEvents(c *gin.Context) {
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	r, e := etlOwner(tx, p, "etl_run", c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 50, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	q := tx.Model(&ETLEvent{}).Where("run_id=?", r.ID)
	if level := c.Query("level"); level != "" {
		if !slices.Contains([]string{"info", "warning", "error"}, level) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("level=?", level)
	}
	if stage := c.Query("stage"); stage != "" {
		if !valid(stage, 40) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("stage=?", stage)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var events []ETLEvent
	if c.Query("download") == "1" {
		if total > 10000 {
			s.fail(c, problem(409, "ETL_LOG_EXPORT_LIMIT", "请按阶段或级别缩小范围后导出，单次最多10,000条"))
			return
		}
		if e = q.Order("id").Limit(10000).Find(&events).Error; e != nil {
			s.fail(c, e)
			return
		}
		if e = s.audit(tx, p, "etl_log_export", r.ID, "success", c.GetString("trace_id")); e != nil {
			s.fail(c, e)
			return
		}
		var out strings.Builder
		for _, event := range events {
			out.Write(encode(event))
			out.WriteByte('\n')
		}
		c.Header("Content-Disposition", "attachment; filename=etl-events.jsonl")
		c.Data(200, "application/x-ndjson", []byte(out.String()))
		return
	}
	if e = q.Order("id DESC").Limit(size).Offset((page - 1) * size).Find(&events).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": events, "total": total, "page": page, "page_size": size})
}
func (s *Server) etlAlerts(c *gin.Context) {
	p := principal(c)
	q := s.db.WithContext(c.Request.Context()).Model(&ETLAlert{}).Joins("JOIN business_records r ON r.kind='etl_run' AND r.id=etl_alerts.run_id")
	if !admin(p) {
		q = q.Where("r.owner_id=?", p.UserID)
	}
	if state := c.Query("state"); state != "" {
		if !slices.Contains([]string{"open", "acknowledged", "resolved"}, state) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("etl_alerts.state=?", state)
	}
	if id := c.Query("run_id"); id != "" {
		q = q.Where("run_id=?", id)
	}
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var items []ETLAlert
	if e = q.Select("etl_alerts.*").Order("etl_alerts.created_at DESC").Limit(size).Offset((page - 1) * size).Find(&items).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// A separate control row prevents progress updates from losing cancellation requests.
func (s *Server) etlProgress(row *Record, run *ETLRun, p identity.Principal, state, message, code string, expectedStates ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, e := get(tx, "etl_run", row.ID, true)
		if e != nil {
			return e
		}
		if len(expectedStates) > 0 && !slices.Contains(expectedStates, current.State) {
			return conflict
		}
		if etlTerminal(current.State) && current.State != state {
			return conflict
		}
		var ctl ETLControl
		e = tx.Where("run_id=?", row.ID).Take(&ctl).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if run.leaseOwner != "" {
			if e = etlLeaseValid(tx, row.ID, run.leaseOwner); e != nil {
				return e
			}
		}
		if state == "committing" && ctl.CancelRequested {
			return context.Canceled
		}
		now := time.Now().UTC()
		if run.StartedAt == nil {
			run.StartedAt = &now
		}
		if etlTerminal(state) || state == "unknown" {
			run.FinishedAt = &now
		}
		run.Message = message
		run.ErrorCode = code
		row.State = state
		row.Body = encode(run)
		if e = save(tx, row); e != nil {
			return e
		}
		level := "info"
		if state == "failed" || state == "unknown" {
			level = "error"
		}
		if state == "cancelled" {
			level = "warning"
		}
		if e = etlEvent(tx, *row, p, level, code, message, run.TraceID); e != nil {
			return e
		}
		if e = tx.Model(&ETLControl{}).Where("run_id=?", row.ID).Update("stage_started_at", now).Error; e != nil {
			return e
		}
		if state == "failed" || state == "unknown" {
			if e = etlAlert(tx, *row, code, "error", message); e != nil {
				return e
			}
		}
		if etlTerminal(state) || state == "unknown" {
			if e = s.audit(tx, p, "etl_run_"+state, row.ID, state, run.TraceID); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Server) etlMonitor(ctx context.Context, cancel context.CancelFunc, id, token string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var ctl ETLControl
			if e := s.db.WithContext(ctx).Where("run_id=?", id).Take(&ctl).Error; e != nil {
				cancel()
				return
			}
			if ctl.CancelRequested {
				cancel()
				return
			}
			result := s.db.WithContext(ctx).Model(&ETLControl{}).Where("run_id=? AND lease_owner=? AND active=true AND lease_expires_at>CURRENT_TIMESTAMP", id, token).Updates(map[string]interface{}{"heartbeat_at": gorm.Expr("CURRENT_TIMESTAMP"), "lease_expires_at": gorm.Expr("CURRENT_TIMESTAMP + interval '30 seconds'")})
			if result.Error != nil || result.RowsAffected != 1 {
				cancel()
				return
			}
			if time.Since(ctl.StageStartedAt) > time.Minute {
				var r Record
				if e := s.db.WithContext(ctx).Where("kind='etl_run' AND id=?", id).Take(&r).Error; e == nil {
					if e = etlAlert(s.db.WithContext(ctx), r, "NO_PROGRESS", "warning", "超过一分钟未更新处理进度，请检查依赖与任务状态"); e != nil {
						cancel()
						return
					}
				}
			}
		}
	}
}
func (s *Server) etlRecovery(ctx context.Context) error {
	if e := s.etlRecoverExpired(ctx); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Where("created_at < ?", time.Now().Add(-90*24*time.Hour)).Delete(&ETLEvent{}).Error
}

func etlFailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "RUN_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "RUN_CANCELLED"
	}
	var p *Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return "RUN_FAILED"
}
