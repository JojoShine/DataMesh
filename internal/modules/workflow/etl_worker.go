package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type etlTargetLock struct {
	TargetKey string `gorm:"primaryKey"`
	RunID     string
}

func (etlTargetLock) TableName() string { return "etl_target_locks" }

func etlTargetKey(tx *gorm.DB, d ETLDefinition) (string, error) {
	r, e := get(tx, "source", d.Target.SourceID, false)
	if e != nil {
		return "", e
	}
	src, e := decode[SourceSecret](r.Body)
	if e != nil {
		return "", e
	}
	return etlTargetFingerprint(src, d.Target), nil
}

// Conservatively serialize equal database/schema/table names across hosts, so aliases cannot bypass unknown results.
func etlTargetFingerprint(src SourceSecret, target TableConfig) string {
	names := []string{src.Kind, src.Database, target.Schema, target.Table}
	if src.Kind == "mysql" {
		for i := range names {
			names[i] = strings.ToLower(names[i])
		}
	}
	key := sha256.Sum256(encode(names))
	return hex.EncodeToString(key[:])
}

// Queued records survive API disconnects and process restarts; only workers claim them.
func (s *Server) StartETLWorkers(parent context.Context) func() {
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
				if e := s.etlRecoverExpired(ctx); e != nil && ctx.Err() == nil {
					slog.Error("ETL lease recovery unavailable")
				}
			}
		}
	}()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				select {
				case s.work <- struct{}{}:
				case <-ctx.Done():
					return
				}
				row, token, ok, e := s.etlClaimNext(ctx)
				if e != nil && ctx.Err() == nil {
					slog.Error("ETL queue claim unavailable")
				}
				if ok {
					func() {
						defer func() {
							if recover() != nil {
								slog.Error("ETL executor panic", "run_id", row.ID)
							}
						}()
						s.executeETL(ctx, row, token)
					}()
				}
				<-s.work
				if !ok {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}
		}()
	}
	return func() { cancel(); wg.Wait() }
}
func (s *Server) etlClaimNext(ctx context.Context) (row Record, token string, claimed bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize global capacity and target reservation, not the data exchange itself.
		if e := tx.Exec("SELECT pg_advisory_xact_lock(619483208)").Error; e != nil {
			return e
		}
		var active int64
		if e := tx.Model(&ETLControl{}).Where("active=true AND lease_expires_at>CURRENT_TIMESTAMP").Count(&active).Error; e != nil {
			return e
		}
		if active >= 2 {
			return nil
		}
		var queued []Record
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "business_records"}, Options: "SKIP LOCKED"}).Joins("LEFT JOIN etl_run_controls ctl ON ctl.run_id=business_records.id LEFT JOIN etl_target_locks occupied ON occupied.target_key=ctl.target_key").Where("business_records.kind='etl_run' AND business_records.state='queued' AND occupied.run_id IS NULL AND NOT EXISTS (SELECT 1 FROM business_records unresolved JOIN etl_run_controls blocked ON blocked.run_id=unresolved.id WHERE unresolved.kind='etl_run' AND unresolved.state='unknown' AND blocked.target_key=ctl.target_key AND blocked.target_key<>'')").Order("business_records.created_at,business_records.id").Limit(100).Find(&queued).Error; e != nil {
			return e
		}
		for _, candidate := range queued {
			run, e := decode[ETLRun](candidate.Body)
			if e != nil {
				return e
			}
			key, e := etlTargetKey(tx, run.Definition)
			if e != nil {
				// Invalid legacy definitions terminate safely without taking a target slot.
				candidate.State = "failed"
				run.Message = "目标配置不可用，未执行交换"
				run.ErrorCode = "ETL_TARGET_INVALID"
				now := time.Now().UTC()
				run.FinishedAt = &now
				candidate.Body = encode(run)
				if e = save(tx, &candidate); e != nil {
					return e
				}
				if e = etlEvent(tx, candidate, identity.Principal{UserID: candidate.OwnerID}, "error", run.ErrorCode, run.Message, run.TraceID); e != nil {
					return e
				}
				if e = etlAlert(tx, candidate, run.ErrorCode, "error", run.Message); e != nil {
					return e
				}
				continue
			}
			reservation := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&etlTargetLock{TargetKey: key, RunID: candidate.ID})
			if reservation.Error != nil {
				return reservation.Error
			}
			if reservation.RowsAffected == 0 {
				continue
			}
			token, e = newID()
			if e != nil {
				return e
			}
			var now time.Time
			if e = tx.Raw("SELECT CURRENT_TIMESTAMP").Scan(&now).Error; e != nil {
				return e
			}
			ctl := ETLControl{RunID: candidate.ID, Active: true, LeaseOwner: token, LeaseExpiresAt: now.Add(30 * time.Second), HeartbeatAt: now, StageStartedAt: now, TargetKey: key}
			if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}}, DoUpdates: clause.AssignmentColumns([]string{"active", "lease_owner", "lease_expires_at", "heartbeat_at", "stage_started_at", "target_key"})}).Create(&ctl).Error; e != nil {
				return e
			}
			candidate.State = "extracting"
			if e = save(tx, &candidate); e != nil {
				return e
			}
			row = candidate
			claimed = true
			return nil
		}
		return nil
	})
	return row, token, claimed && err == nil, err
}
func (s *Server) etlRecoverExpired(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []Record
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "business_records"}, Options: "SKIP LOCKED"}).Joins("JOIN etl_run_controls ctl ON ctl.run_id=business_records.id").Where("business_records.kind='etl_run' AND business_records.state IN ? AND ((ctl.active=true AND ctl.lease_expires_at<=CURRENT_TIMESTAMP) OR (ctl.active=false AND ctl.lease_owner<>''))", []string{"extracting", "loading", "committing"}).Limit(20).Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			run, e := decode[ETLRun](r.Body)
			if e != nil {
				return e
			}
			r.State = "unknown"
			run.ErrorCode = "LEASE_EXPIRED"
			run.Message = "执行租约过期，须核实目标事务；禁止自动重跑"
			now := time.Now().UTC()
			run.FinishedAt = &now
			r.Body = encode(run)
			if e = save(tx, &r); e != nil {
				return e
			}
			if e = tx.Model(&ETLControl{}).Where("run_id=?", r.ID).Update("active", false).Error; e != nil {
				return e
			}
			if e = etlEvent(tx, r, identity.Principal{UserID: r.OwnerID}, "error", run.ErrorCode, run.Message, run.TraceID); e != nil {
				return e
			}
			if e = etlAlert(tx, r, run.ErrorCode, "error", run.Message); e != nil {
				return e
			}
		}
		return tx.Exec("DELETE FROM etl_target_locks l USING business_records r,etl_run_controls c WHERE r.kind='etl_run' AND r.id=l.run_id AND c.run_id=r.id AND c.active=false AND r.state IN ('succeeded','failed','cancelled')").Error
	})
}
func (s *Server) etlFinishLease(id, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, e := get(tx, "etl_run", id, true)
		if e != nil {
			return e
		}
		result := tx.Model(&ETLControl{}).Where("run_id=? AND lease_owner=?", id, token).Update("active", false)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 && etlTerminal(r.State) {
			return tx.Where("run_id=?", id).Delete(&etlTargetLock{}).Error
		}
		return nil
	})
	if e != nil {
		slog.Error("ETL lease close unavailable", "run_id", id)
	}
}
func (s *Server) etlPrincipal(ctx context.Context, id string) (identity.Principal, error) {
	p := identity.Principal{UserID: id}
	var u identity.User
	if e := s.db.WithContext(ctx).Where("id=? AND active=true", id).Take(&u).Error; e != nil {
		return p, e
	}
	p.DepartmentID = u.DepartmentID
	p.Username = u.Username
	if e := s.db.WithContext(ctx).Model(&identity.UserRole{}).Where("user_id=?", id).Pluck("role", &p.Roles).Error; e != nil {
		return p, e
	}
	e := s.db.WithContext(ctx).Model(&identity.PlatformAccess{}).Where("user_id=?", id).Pluck("platform", &p.Platforms).Error
	return p, e
}
func etlLeaseValid(tx *gorm.DB, id, token string) error {
	var n int64
	if e := tx.Model(&ETLControl{}).Where("run_id=? AND lease_owner=? AND active=true AND lease_expires_at>CURRENT_TIMESTAMP", id, token).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return problem(409, "ETL_LEASE_LOST", "执行租约失效，禁止继续提交")
	}
	return nil
}

func (s *Server) etlBackfillTargets(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(619483208)").Error; e != nil {
			return e
		}
		var rows []Record
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "business_records"}}).Joins("LEFT JOIN etl_run_controls ctl ON ctl.run_id=business_records.id").Where("business_records.kind='etl_run' AND business_records.state IN ? AND (ctl.run_id IS NULL OR ctl.target_key='')", []string{"queued", "extracting", "loading", "committing", "unknown"}).Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			run, e := decode[ETLRun](r.Body)
			if e != nil {
				return e
			}
			if run.Definition.Target.SourceID == "" || run.Definition.Target.Table == "" {
				continue
			}
			key, e := etlTargetKey(tx, run.Definition)
			if e != nil {
				return e
			}
			legacyActive := slices.Contains([]string{"extracting", "loading", "committing"}, r.State)
			ctl := ETLControl{RunID: r.ID, TargetKey: key, Active: legacyActive}
			if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{"target_key": key, "active": legacyActive})}).Create(&ctl).Error; e != nil {
				return e
			}
			if r.State != "queued" {
				if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&etlTargetLock{TargetKey: key, RunID: r.ID}).Error; e != nil {
					return e
				}
			}
		}
		return nil
	})
}
