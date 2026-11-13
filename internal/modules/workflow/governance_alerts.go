package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceAlertRule struct {
	WorkspaceID  *string   `json:"workspace_id"`
	ID           string    `json:"id" gorm:"primaryKey"`
	DepartmentID string    `json:"department_id"`
	ActorID      string    `json:"actor_id"`
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	Failure      bool      `json:"failure"`
	QualityRate  float64   `json:"quality_rate"`
	Webhook      string    `json:"webhook"`
	CursorAt     time.Time `json:"-"`
	CursorID     string    `json:"-"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (GovernanceAlertRule) TableName() string { return "governance_alert_rules" }

type GovernanceAlert struct {
	ID          string     `json:"id" gorm:"primaryKey"`
	RuleID      string     `json:"rule_id"`
	RunID       string     `json:"run_id"`
	Code        string     `json:"code"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	NextAt      time.Time  `json:"next_at"`
	LeaseUntil  time.Time  `json:"-"`
	ErrorCode   string     `json:"error_code"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

func (GovernanceAlert) TableName() string { return "governance_alerts" }
func (s *Server) registerGovernanceAlerts(api, write *gin.RouterGroup) {
	api.GET("/governance/alert-rules", s.require("governance", "governor"), func(c *gin.Context) {
		q := workspaceQuery(s.db.WithContext(c.Request.Context()), "workspace_id")
		p := principal(c)
		rows := []GovernanceAlertRule{}
		if !admin(p) {
			q = q.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		if e := q.Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "limit": 100})
	})
	api.GET("/governance/alerts", s.require("governance", "governor"), func(c *gin.Context) {
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
		p := principal(c)
		q := s.db.WithContext(c.Request.Context()).Model(&GovernanceAlert{}).Where("EXISTS(SELECT 1 FROM governance_alert_rules r WHERE r.id=governance_alerts.rule_id AND r.actor_id=? AND r.department_id=?)", p.UserID, p.DepartmentID)
		if admin(p) {
			q = s.db.WithContext(c.Request.Context()).Model(&GovernanceAlert{})
		}
		q = workspaceQuery(q, "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_alerts.run_id)").Where("(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_alerts.run_id) IS NOT NULL")
		var total int64
		rows := []GovernanceAlert{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	for _, path := range []string{"/governance/alert-rules", "/governance/alert-rules/:id"} {
		write.POST(path, s.require("governance", "governor"), func(c *gin.Context) {
			id := c.Param("id")
			s.mutate("governance_alert_rule", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
				input, e := bind[struct {
					Name            string  `json:"name"`
					Enabled         bool    `json:"enabled"`
					Failure         bool    `json:"failure"`
					QualityRate     float64 `json:"quality_rate"`
					Webhook         string  `json:"webhook"`
					ExpectedVersion int     `json:"expected_version"`
				}](b)
				if e != nil {
					return 0, nil, id, e
				}
				if !valid(input.Name, 128) || input.QualityRate < 0 || input.QualityRate > 1 || len(input.Webhook) > 2048 {
					return 0, nil, id, invalid
				}
				if input.Webhook != "" {
					client, e := apiTransport(tx.Statement.Context, input.Webhook)
					if e != nil {
						return 0, nil, id, e
					}
					client.CloseIdleConnections()
				}
				row := GovernanceAlertRule{DepartmentID: p.DepartmentID, ActorID: p.UserID, Version: 1, CursorAt: time.Now().UTC()}
				if scope := workspaceScope(tx.Statement.Context); scope.ID != "" {
					space, e := governanceWorkspaceWrite(tx, p, scope.ID)
					if e != nil {
						return 0, nil, id, e
					}
					row.WorkspaceID = &space.ID
				}
				status := 201
				if id != "" {
					if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; e != nil {
						return 0, nil, id, e
					}
					if !admin(p) && (row.ActorID != p.UserID || row.DepartmentID != p.DepartmentID) {
						return 0, nil, id, denied
					}
					if row.WorkspaceID != nil {
						if _, e = governanceWorkspaceWrite(tx, p, *row.WorkspaceID); e != nil {
							return 0, nil, id, e
						}
					} else if workspaceScope(tx.Statement.Context).ID != "" {
						return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_MISMATCH", "这是跨空间的旧告警规则，请从原管理入口维护")
					}
					if row.Version != input.ExpectedVersion {
						return 0, nil, id, conflict
					}
					row.Version++
					status = 200
				} else {
					id, e = newID()
					if e != nil {
						return 0, nil, id, e
					}
					row.ID = id
				}
				if !row.Enabled && input.Enabled {
					row.CursorAt = time.Now().UTC()
					row.CursorID = ""
				}
				row.Name = input.Name
				row.Enabled = input.Enabled
				row.Failure = input.Failure
				row.QualityRate = input.QualityRate
				row.Webhook = input.Webhook
				row.UpdatedAt = time.Now().UTC()
				if e = tx.Save(&row).Error; e != nil {
					return 0, nil, id, e
				}
				return status, encode(row), id, nil
			})(c)
		})
	}
}

// The cursor advances with each evaluated terminal run. Deduplication and cursor share one transaction.
func (s *Server) sweepGovernanceAlerts(ctx context.Context) error {
	rules := []GovernanceAlertRule{}
	if e := s.db.WithContext(ctx).Where("enabled=true").Order("updated_at,id").Limit(1000).Find(&rules).Error; e != nil {
		return e
	}
	for _, rule := range rules {
		actor, e := s.etlPrincipal(ctx, rule.ActorID)
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			return e
		}
		if !admin(actor) && (!slices.Contains(actor.Roles, "governor") || !slices.Contains(actor.Platforms, "governance") || actor.DepartmentID != rule.DepartmentID) {
			continue
		}
		if e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var r GovernanceAlertRule
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("id=? AND enabled=true", rule.ID).Take(&r).Error; e != nil {
				if e == gorm.ErrRecordNotFound {
					return nil
				}
				return e
			}
			runs := []GovernanceRun{}
			runTx := tx.Where("workspace_id IS NOT NULL")
			if r.WorkspaceID != nil {
				runTx = runTx.Where("workspace_id=?", *r.WorkspaceID)
			}
			if e := runTx.Where("actor_id=? AND department_id=? AND state IN ('succeeded','failed','interrupted','cancelled') AND (updated_at,id)>(?,?)", r.ActorID, r.DepartmentID, r.CursorAt, r.CursorID).Order("updated_at,id").Limit(200).Find(&runs).Error; e != nil {
				return e
			}
			for _, run := range runs {
				codes := []string{}
				if r.Failure && slices.Contains([]string{"failed", "interrupted"}, run.State) {
					codes = append(codes, "RUN_FAILED")
				}
				var counts struct {
					Checked int64
					Passed  int64
				}
				if e := tx.Model(&GovernanceRunEvent{}).Select("COALESCE(sum(input_count),0) checked,COALESCE(sum(output_count),0) passed").Where("run_id=? AND code='QUALITY_CHECKED'", run.ID).Scan(&counts).Error; e != nil {
					return e
				}
				if counts.Checked > 0 && float64(counts.Checked-counts.Passed)/float64(counts.Checked) > r.QualityRate {
					codes = append(codes, "QUALITY_THRESHOLD")
				}
				for _, code := range codes {
					id, e := newID()
					if e != nil {
						return e
					}
					state := "in_app"
					if r.Webhook != "" {
						state = "pending"
					}
					a := GovernanceAlert{ID: id, RuleID: r.ID, RunID: run.ID, Code: code, State: state, NextAt: time.Now().UTC(), LeaseUntil: time.Unix(0, 0)}
					if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&a).Error; e != nil {
						return e
					}
				}
				r.CursorAt = run.UpdatedAt
				r.CursorID = run.ID
			}
			return tx.Model(&r).Updates(map[string]interface{}{"cursor_at": r.CursorAt, "cursor_id": r.CursorID}).Error
		}); e != nil {
			return e
		}
	}
	for range 20 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var alert GovernanceAlert
		var rule GovernanceAlertRule
		e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(state='pending' AND next_at<=now()) OR (state='delivering' AND lease_until<now())").Order("next_at,id").Take(&alert).Error; e != nil {
				return e
			}
			if e := tx.Where("id=?", alert.RuleID).Take(&rule).Error; e != nil {
				return e
			}
			var run GovernanceRun
			if e := tx.Select("workspace_id").Where("id=?", alert.RunID).Take(&run).Error; e != nil {
				return e
			}
			state := "delivering"
			if !rule.Enabled || rule.Webhook == "" || run.WorkspaceID == "" {
				state = "cancelled"
			}
			alert.State = state
			alert.Attempts++
			alert.LeaseUntil = time.Now().Add(time.Minute)
			return tx.Save(&alert).Error
		})
		if e == gorm.ErrRecordNotFound {
			break
		}
		if e != nil {
			return e
		}
		if alert.State == "cancelled" {
			continue
		}
		// Payload excludes row values, schema, credentials and private configuration. Delivery ID supports receiver deduplication.
		payload := encode(gin.H{"delivery_id": alert.ID, "code": alert.Code, "run_id": alert.RunID, "created_at": alert.CreatedAt})
		actor, e := s.etlPrincipal(ctx, rule.ActorID)
		if e == nil && !admin(actor) && (!slices.Contains(actor.Roles, "governor") || !slices.Contains(actor.Platforms, "governance") || actor.DepartmentID != rule.DepartmentID) {
			e = denied
		}
		if e == nil {
			var client *http.Client
			client, e = apiTransport(ctx, rule.Webhook)
			if e == nil {
				req, er := http.NewRequestWithContext(ctx, http.MethodPost, rule.Webhook, bytes.NewReader(payload))
				e = er
				if e == nil {
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Idempotency-Key", alert.ID)
					var resp *http.Response
					resp, e = client.Do(req)
					if e == nil {
						_, er = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
						closeErr := resp.Body.Close()
						if er != nil {
							e = er
						} else if closeErr != nil {
							e = closeErr
						} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
							e = problem(502, "WEBHOOK_REJECTED", "通知接收方未确认")
						}
					}
				}
				client.CloseIdleConnections()
			}
		}
		updates := map[string]interface{}{"state": "delivered", "delivered_at": time.Now().UTC(), "error_code": ""}
		if e != nil {
			state := "pending"
			if alert.Attempts >= 5 {
				state = "failed"
			}
			updates = map[string]interface{}{"state": state, "error_code": "WEBHOOK_DELIVERY_FAILED", "next_at": time.Now().Add(time.Duration(1<<min(alert.Attempts, 5)) * time.Minute)}
		}
		if e = s.db.WithContext(ctx).Model(&GovernanceAlert{}).Where("id=? AND state='delivering' AND attempts=?", alert.ID, alert.Attempts).Updates(updates).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) StartGovernanceMaintenanceWorker(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		run := func() {
			work, done := context.WithTimeout(ctx, 45*time.Second)
			defer done()
			if e := s.sweepGovernanceMonitors(work); e != nil && ctx.Err() == nil {
				slog.Error("Governance monitoring failed", "code", captureErrorCode(e))
			}
			if e := s.sweepGovernanceIssueNotices(work); e != nil && ctx.Err() == nil {
				slog.Error("quality issue notices", "error", e)
			}
			if e := s.sweepGovernanceAlerts(work); e != nil && ctx.Err() == nil {
				slog.Error("Governance alert processing failed", "code", captureErrorCode(e))
			}
			if e := s.sweepGovernanceRetention(work); e != nil && ctx.Err() == nil {
				slog.Error("Governance retention failed", "code", captureErrorCode(e))
			}
			metadataContext, metadataDone := context.WithTimeout(work, 20*time.Second)
			defer metadataDone()
			if e := s.sweepMetadataCollection(metadataContext); e != nil && ctx.Err() == nil {
				slog.Error("Metadata collection failed", "code", captureErrorCode(e))
			}
		}
		run()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
