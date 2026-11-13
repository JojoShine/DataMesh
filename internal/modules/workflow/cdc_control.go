package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CDCControl struct {
	ConfigOwned        bool      `json:"-"`
	TaskID             string    `gorm:"primaryKey" json:"task_id"`
	DesiredState       string    `json:"desired_state"`
	Generation         int       `json:"generation"`
	ObservedGeneration int       `json:"observed_generation"`
	LeaseOwner         string    `json:"-"`
	LeaseExpiresAt     time.Time `json:"-"`
	LastErrorCode      string    `json:"last_error_code,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (CDCControl) TableName() string { return "cdc_controls" }

func (s *Server) queueCDCControl(tx *gorm.DB, p identity.Principal, r Record, action string) (int, json.RawMessage, string, error) {
	var ctl CDCControl
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id=?", r.ID).Take(&ctl).Error; e != nil {
		return 0, nil, r.ID, e
	}
	switch action {
	case "pause":
		ctl.DesiredState = "paused"
	case "start", "resume":
		ctl.DesiredState = "running"
	case "reconcile":
	default:
		return 0, nil, r.ID, invalid
	}
	if e := tx.Model(&Operation{}).Where("resource_id=? AND status='accepted' AND response->>'generation' IS NOT NULL", r.ID).Updates(map[string]interface{}{"status": "failed", "response": encode(gin.H{"code": "CDC_CONTROL_SUPERSEDED", "task_id": r.ID}), "updated_at": time.Now()}).Error; e != nil {
		return 0, nil, r.ID, e
	}
	ctl.Generation++
	ctl.LastErrorCode = ""
	// A leased worker finishes or loses its generation before the next reconciliation.
	if ctl.LeaseOwner == "" {
		ctl.LeaseExpiresAt = time.Now().UTC()
	}
	if e := tx.Save(&ctl).Error; e != nil {
		return 0, nil, r.ID, e
	}
	r.State = "initializing"
	if ctl.DesiredState == "paused" {
		r.State = "pausing"
	}
	if e := save(tx, &r); e != nil {
		return 0, nil, r.ID, e
	}
	return 202, encode(gin.H{"task_id": r.ID, "generation": ctl.Generation, "desired_state": ctl.DesiredState}), r.ID, nil
}

func captureErrorCode(e error) string {
	if e == nil {
		return ""
	}
	var p *Problem
	if errors.As(e, &p) {
		return p.Code
	}
	return "CAPTURE_UNAVAILABLE"
}

func (s *Server) cdcStartConditions(ctx context.Context, src SourceSecret, b CDCBinding) error {
	probe, e := cdcProbeSource(src, b.PDB)
	if e != nil {
		return e
	}
	db, e := s.runtime.database(ctx, s.db, probe)
	if e != nil {
		return e
	}
	defer db.Close()
	fingerprint, e := cdcDatabaseIdentity(ctx, db, src, CDCConfigInput{PDB: b.PDB})
	if e != nil {
		return e
	}
	if fingerprint != b.SourceFingerprint {
		return problem(409, "CDC_SOURCE_CHANGED", "数据库实例身份已变化，请核查任务绑定")
	}
	checks := s.cdcPreflight(ctx, s.db, probe, cdcScope{Schema: b.Schema, Table: b.Table})
	for _, c := range checks {
		if src.Kind == "postgres" && c.Check == "replication_capacity" {
			c = cdcCapacity(ctx, db, c, false, b.Slot)
		}
		if src.Kind == "oracle" && c.Check == "mining_account" {
			c = cdcOracleMiningCheck(ctx, db, b, c)
		}
		if c.Check == "platform_cdc" || c.Check == "publication" {
			continue
		}
		if !c.Passed {
			return problem(409, "CDC_CONDITIONS_REQUIRED", "数据库采集条件尚未通过，请查看所选表的 CDC 条件诊断")
		}
	}
	if src.Kind == "postgres" {
		var contains bool
		if e = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_publication_tables WHERE pubname=$1 AND schemaname=$2 AND tablename=$3)", b.Publication, b.Schema, b.Table).Scan(&contains); e != nil {
			return problem(409, "CDC_PUBLICATION_UNVERIFIED", "无法读取发布范围，请 DBA 核对所选表的发布配置")
		}
		if !contains {
			return problem(409, "CDC_PUBLICATION_REQUIRED", "指定发布未包含所选表，请 DBA 配置后重试")
		}
	}
	table, e := discoverTable(ctx, db, src, b.Schema, b.Table)
	if e != nil {
		return e
	}
	object, e := get(s.db.WithContext(ctx), "object", b.ObjectID, false)
	if e != nil {
		return e
	}
	declared, e := decode[Object](object.Body)
	if e != nil {
		return e
	}
	for i := range table.Fields {
		for _, field := range declared.Fields {
			if table.Fields[i].Name == field.Name {
				table.Fields[i].Sensitive = field.Sensitive
			}
		}
	}
	if len(table.Keys) == 0 || string(encode(table.Fields)) != string(encode(declared.Fields)) {
		return problem(409, "CDC_SCHEMA_CHANGED", "所选表结构已变化，已阻断采集，请核查目录契约及授权")
	}
	return nil
}

// Called outside a database transaction; generation checks fence stale observations.
func (s *Server) reconcileCDCControl(parent context.Context, taskID string) error {
	token, e := newID()
	if e != nil {
		return e
	}
	var ctl CDCControl
	claimed := false
	e = s.db.WithContext(parent).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id=?", taskID).Take(&ctl).Error; err != nil {
			return err
		}
		if ctl.DesiredState == "draft" || ctl.LeaseExpiresAt.After(time.Now()) {
			return nil
		}
		ctl.LeaseOwner = token
		ctl.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		if err := tx.Save(&ctl).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if e != nil || !claimed {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	task, e := get(s.db.WithContext(ctx), "task", taskID, false)
	if e != nil {
		return e
	}
	var b CDCBinding
	if e = s.db.WithContext(ctx).Where("task_id=?", taskID).Take(&b).Error; e != nil {
		return problem(503, "CDC_BINDING_UNAVAILABLE", "采集绑定或凭据暂不可用，请核查")
	}
	source, e := get(s.db.WithContext(ctx), "source", b.SourceID, false)
	if e != nil {
		return e
	}
	src, e := decode[SourceSecret](source.Body)
	if e != nil {
		return e
	}
	credential, e := s.runtime.open(src.Ciphertext)
	if e != nil {
		return problem(503, "CDC_CREDENTIAL_UNAVAILABLE", "采集绑定或凭据暂不可用，请核查")
	}
	cfg, e := buildCDCConfig(b, src, credential)
	if e != nil {
		return problem(503, "CDC_CONFIG_INVALID", "采集绑定或凭据暂不可用，请核查")
	}
	p, permission := s.etlPrincipal(ctx, task.OwnerID)
	if permission == nil && (!platform(p, "exchange") || !role(p, "operator")) {
		permission = denied
	}
	if permission == nil {
		permission = requireResourceCapability(s.db.WithContext(ctx), p, Resource{Type: "source", ID: b.SourceID}, "operate_resource")
	}
	if permission == nil {
		permission = s.runtime.allowSource(ctx, s.db, SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database})
	}
	if source.Version != b.SourceVersion {
		permission = problem(409, "SOURCE_VERSION_CHANGED", "数据源配置已变化，请核查绑定")
	}
	actualDesired := ctl.DesiredState
	if permission != nil {
		actualDesired = "paused"
	}
	state := "manual_required"
	controlErr := permission
	prefix := "/connectors/" + url.PathEscape(b.ConnectorName)
	configRaw, lookupErr := s.runtime.request(ctx, "GET", prefix+"/config", nil)
	exists := lookupErr == nil
	owned := ctl.ConfigOwned
	if lookupErr != nil && captureErrorCode(lookupErr) != "CAPTURE_NOT_FOUND" {
		controlErr = lookupErr
	}
	if exists {
		var existing map[string]string
		if json.Unmarshal(configRaw, &existing) != nil {
			controlErr = problem(409, "CDC_CONFIG_UNVERIFIED", "无法核对采集作业配置")
		} else {
			for key, value := range cfg {
				if existing[key] != value {
					controlErr = problem(409, "CDC_CONFIG_CONFLICT", "采集作业配置与平台绑定不一致，请核查；不会覆盖已有作业")
					exists = false
					break
				}
			}
		}
	}
	if exists && captureErrorCode(controlErr) != "CDC_CONFIG_UNVERIFIED" {
		owned = true
	}
	if !exists && owned && captureErrorCode(controlErr) == "CDC_CONFIG_CONFLICT" {
		exists = true
		actualDesired = "paused"
	}
	// Previously verified ownership permits an emergency pause after source/configuration drift; never overwrite or resume drifted jobs.

	if exists && captureErrorCode(controlErr) != "CDC_CONFIG_UNVERIFIED" {
		if actualDesired == "running" && controlErr == nil {
			controlErr = s.cdcStartConditions(ctx, src, b)
			if controlErr != nil {
				actualDesired = "paused"
			}
		}
		method := "resume"
		if actualDesired == "paused" {
			method = "pause"
		}
		var current ConnectorStatus
		current, e = s.runtime.connectorByName(ctx, b.ConnectorName)
		expected := "RUNNING"
		if method == "pause" {
			expected = "PAUSED"
		}
		failed := current.Connector.State == "FAILED"
		for _, t := range current.Tasks {
			if t.State == "FAILED" {
				failed = true
			}
		}
		if e == nil && method == "resume" && failed {
			if ctl.ObservedGeneration < ctl.Generation {
				if e = s.cdcControlFence(ctx, ctl, token); e == nil {
					_, e = s.runtime.request(ctx, "POST", prefix+"/restart?includeTasks=true&onlyFailed=true", nil)
				}
			} else {
				e = problem(409, "CDC_TASK_FAILED", "采集任务失败，请核查诊断后执行重新核对以恢复；不会重置位点")
			}
		} else if e == nil && current.Connector.State == expected && len(current.Tasks) > 0 {
			for _, t := range current.Tasks {
				if t.State != expected {
					e = problem(409, "CONTROL_UNKNOWN", "采集任务状态尚未一致")
				}
			}
		} else if err := s.cdcControlFence(ctx, ctl, token); err != nil {
			e = err
		} else {
			_, e = s.runtime.request(ctx, "PUT", prefix+"/"+method, nil)
		}
		if e != nil {
			controlErr = e
		} else if method == "pause" {
			state = "paused"
		} else {
			state = "initializing"
		}
		// A request accepted by Connect is not proof of a running connector.
		if e == nil {
			current, e = s.runtime.connectorByName(ctx, b.ConnectorName)
			if e == nil && running(current) && actualDesired == "running" {
				state = "streaming"
			} else if e == nil && cdcPaused(current) && actualDesired == "paused" {
				state = "paused"
			} else {
				state = "initializing"
			}
		}
	} else if !exists && lookupErr != nil && captureErrorCode(lookupErr) == "CAPTURE_NOT_FOUND" && actualDesired == "running" && controlErr == nil {
		controlErr = s.cdcStartConditions(ctx, src, b)
		if controlErr == nil && src.Kind == "oracle" {
			check := s.cdcOracleWorkTableCheck(ctx, s.db, src)
			if !check.Passed {
				controlErr = problem(409, "CDC_WORKTABLE_REQUIRED", "Oracle 新任务工作表创建条件未通过，请查看 CREATE TABLE 权限及默认表空间额度诊断")
			}
		}
		if controlErr == nil {
			check := s.cdcConnectorValidation(ctx, src, b)
			if !check.Passed {
				controlErr = problem(409, "CDC_CONFIG_REJECTED", "采集服务未通过配置校验，请核对数据库条件与驱动")
			}
		}
		if controlErr == nil && src.Kind == "postgres" {
			var dbClose func()
			_, _, db, err := s.sourceForRead(ctx, s.db, p, b.SourceID)
			if err != nil {
				controlErr = err
			} else {
				dbClose = func() { db.Close() }
				var slot bool
				err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name=$1)", b.Slot).Scan(&slot)
				dbClose()
				if err != nil || slot {
					controlErr = problem(409, "CDC_SLOT_RESERVED", "复制槽已存在但未找到所属采集作业，请管理员核查，禁止重置位点")
				}
			}
		}
		if controlErr == nil {
			controlErr = s.cdcControlFence(ctx, ctl, token)
		}
		if controlErr == nil {
			_, controlErr = s.runtime.request(ctx, "PUT", prefix+"/config", encode(cfg))
			state = "initializing"
		}
	} else if !exists && captureErrorCode(lookupErr) == "CAPTURE_NOT_FOUND" && actualDesired == "paused" {
		state = "paused"
	}
	if controlErr != nil && state != "paused" {
		state = "manual_required"
	}
	finishCtx, done := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer done()
	e = s.db.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		var current CDCControl
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id=?", taskID).Take(&current).Error; err != nil {
			return err
		}
		if current.LeaseOwner != token {
			return problem(409, "CDC_LEASE_LOST", "采集控制租约已失效")
		}
		current.ConfigOwned = owned
		current.LeaseOwner = ""
		current.LeaseExpiresAt = time.Now().Add(3 * time.Second)
		if current.Generation != ctl.Generation {
			current.LeaseExpiresAt = time.Now()
			return tx.Save(&current).Error
		}
		current.LastErrorCode = captureErrorCode(controlErr)
		current.ObservedGeneration = ctl.Generation
		if permission != nil || captureErrorCode(controlErr) == "CDC_CONFIG_CONFLICT" {
			current.DesiredState = "paused"
		}
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		if err := tx.Model(&Record{}).Where("kind='task' AND id=?", taskID).Update("state", state).Error; err != nil {
			return err
		}
		// Archive publication owns other task fields and can finish during the external request.
		if err := tx.Model(&Record{}).Where("kind='task' AND id=?", taskID).Update("body", gorm.Expr("jsonb_set(body,'{error_code}',to_jsonb(?::text))", current.LastErrorCode)).Error; err != nil {
			return err
		}
		if state == "streaming" || state == "paused" || (state == "manual_required" && cdcControlFailureFinal(controlErr)) {
			status := "succeeded"
			if controlErr != nil {
				status = "failed"
			}
			if err := tx.Model(&Operation{}).Where("resource_id=? AND status='accepted' AND response->>'generation'=?", taskID, strconv.Itoa(ctl.Generation)).Updates(map[string]interface{}{"status": status, "response": encode(gin.H{"task_id": taskID, "state": state, "code": current.LastErrorCode}), "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		if task.State != state || current.LastErrorCode != "" {
			return s.audit(tx, identity.Principal{UserID: task.OwnerID}, "cdc_control", taskID, state, "")
		}
		return nil
	})
	return e
}

func (s *Server) cdcControlFence(ctx context.Context, ctl CDCControl, token string) error {
	var count int64
	if e := s.db.WithContext(ctx).Model(&CDCControl{}).Where("task_id=? AND generation=? AND lease_owner=? AND lease_expires_at>CURRENT_TIMESTAMP", ctl.TaskID, ctl.Generation, token).Count(&count).Error; e != nil {
		return e
	}
	if count != 1 {
		return problem(409, "CDC_LEASE_LOST", "采集配置或控制租约已变化，请核查实际状态")
	}
	return nil
}

func (s *Server) StartCDCWorkers(parent context.Context) func() {
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
				var controls []CDCControl
				if e := s.db.WithContext(ctx).Where("desired_state<>'draft' AND lease_expires_at<CURRENT_TIMESTAMP").Order("updated_at").Limit(8).Find(&controls).Error; e != nil {
					if ctx.Err() == nil {
						slog.Error("CDC control queue unavailable")
					}
					continue
				}
				for _, ctl := range controls {
					if ctx.Err() != nil {
						return
					}
					if e := s.reconcileCDCControl(ctx, ctl.TaskID); e != nil && ctx.Err() == nil {
						slog.Error("CDC control reconciliation unavailable", "task_id", ctl.TaskID, "code", captureErrorCode(e))
					}
				}
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func cdcControlFailureFinal(err error) bool {
	var p *Problem
	if !errors.As(err, &p) || p.Status < 400 || p.Status >= 500 {
		return false
	}
	return p.Code != "CONTROL_UNKNOWN" && p.Code != "CDC_LEASE_LOST" && p.Code != "CDC_TOPIC_PENDING"
}

func cdcPaused(state ConnectorStatus) bool {
	if state.Connector.State != "PAUSED" {
		return false
	}
	for _, task := range state.Tasks {
		if task.State != "PAUSED" {
			return false
		}
	}
	return true
}
