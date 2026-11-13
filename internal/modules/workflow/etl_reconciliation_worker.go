package workflow

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) claimETLReconciliation(ctx context.Context) (ETLReconciliation, string, bool, error) {
	var report ETLReconciliation
	token, e := newID()
	if e != nil {
		return report, "", false, e
	}
	claimed := false
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reports []ETLReconciliation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "etl_reconciliations"}, Options: "SKIP LOCKED"}).Joins("JOIN business_records run ON run.kind='etl_run' AND run.id=etl_reconciliations.run_id").Where("run.state IN ('succeeded','failed','cancelled') AND (etl_reconciliations.state='pending' OR (etl_reconciliations.state='checking' AND etl_reconciliations.lease_expires_at<CURRENT_TIMESTAMP))").Order("etl_reconciliations.created_at").Limit(1).Find(&reports).Error; e != nil {
			return e
		}
		if len(reports) == 0 {
			return nil
		}
		report = reports[0]
		report.State = "checking"
		report.LeaseOwner = token
		report.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		if e := tx.Save(&report).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	return report, token, claimed, e
}
func (s *Server) reconcileETLDelivery(parent context.Context, report ETLReconciliation, token string) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.etl.Timeout)
	defer cancel()
	var monitor sync.WaitGroup
	done := make(chan struct{})
	monitor.Add(1)
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				result := s.db.WithContext(ctx).Model(&ETLReconciliation{}).Where("id=? AND lease_owner=? AND state='checking' AND lease_expires_at>CURRENT_TIMESTAMP", report.ID, token).Updates(map[string]interface{}{"lease_expires_at": time.Now().Add(90 * time.Second), "updated_at": time.Now()})
				if result.Error != nil || result.RowsAffected != 1 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(done); monitor.Wait() }()
	result := report
	record, e := get(s.db.WithContext(ctx), "etl_run", report.RunID, false)
	var run ETLRun
	if e == nil {
		run, e = decode[ETLRun](record.Body)
	}
	if run.InputSnapshot != nil {
		run.authorization = run.InputSnapshot.Authorization
	}
	if e == nil && record.State != "succeeded" {
		e = problem(409, "DELIVERY_NOT_COMMITTED", "执行未提交，未进行提交后对账")
	}
	if e == nil {
		p, err := s.etlPrincipal(ctx, record.OwnerID)
		e = err
		if e == nil {
			e = s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization)
		}
		if e == nil {
			_, src, db, err := s.sourceForRead(ctx, s.db, p, run.Definition.Target.SourceID)
			e = err
			if e == nil {
				func() {
					defer db.Close()
					tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
					if err != nil {
						e = err
						return
					}
					defer tx.Rollback()
					if e = etlSessionTimeZone(ctx, tx, src.Kind); e != nil {
						return
					}
					lastProgress := time.Time{}
					result, e = s.checkETLDelivery(ctx, report.RunID, tx, func(partial ETLReconciliation) error {
						if !lastProgress.IsZero() && time.Since(lastProgress) < time.Second {
							return nil
						}
						lastProgress = time.Now()
						saved := s.db.WithContext(ctx).Model(&ETLReconciliation{}).Where("id=? AND lease_owner=? AND state='checking' AND lease_expires_at>CURRENT_TIMESTAMP", report.ID, token).Updates(map[string]interface{}{"checked_rows": partial.CheckedRows, "mismatch_count": partial.MismatchCount, "updated_at": time.Now()})
						if saved.Error != nil {
							return saved.Error
						}
						if saved.RowsAffected != 1 {
							return problem(409, "RECONCILIATION_LEASE_LOST", "对账租约已失效")
						}
						return nil
					})
				}()
			}
		}
	}
	result.TaskVersion = run.TaskVersion
	result.ReadRows = int64(run.Read)
	result.ProcessedRows = int64(run.Read)
	if result.KeyCount == 0 {
		result.KeyCount = report.KeyCount
	}
	if run.InputSnapshot != nil {
		result.InputSnapshotID = run.InputSnapshot.SnapshotID
		result.InputScope = encode(run.InputSnapshot)
	}
	result.ID = report.ID
	result.Version = report.Version
	result.RunID = report.RunID
	if e != nil {
		result.State = "failed"
		result.ErrorCode = etlFailureCode(e)
	}
	now := time.Now().UTC()
	result.CheckedAt = &now
	result.DurationMS = time.Since(started).Milliseconds()
	finish, doneFinish := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer doneFinish()
	if parent.Err() != nil {
		if err := s.db.WithContext(finish).Model(&ETLReconciliation{}).Where("id=? AND lease_owner=? AND state='checking'", report.ID, token).Updates(map[string]interface{}{"state": "pending", "lease_owner": "", "lease_expires_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
			slog.Error("Delivery reconciliation requeue unavailable", "run_id", report.RunID)
		}
		return
	}
	finalErr := s.db.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		var current ETLReconciliation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND lease_owner=? AND state='checking' AND lease_expires_at>CURRENT_TIMESTAMP", report.ID, token).Take(&current).Error; err != nil {
			return err
		}
		latest, err := get(tx, "etl_run", report.RunID, false)
		if err != nil {
			return err
		}
		if result.State == "passed" || result.State == "mismatched" {
			recipient, err := s.etlPrincipal(finish, latest.OwnerID)
			if err != nil {
				result.State = "failed"
				result.ErrorCode = "FORBIDDEN"
			} else if err = s.etlPermission(tx, recipient, run.Definition, run.authorization); err != nil {
				result.State = "failed"
				result.ErrorCode = etlFailureCode(err)
			}
			if latest.State != "succeeded" {
				result.State = "failed"
				result.ErrorCode = "DELIVERY_NOT_COMMITTED"
			}
		}
		result.CreatedAt = current.CreatedAt
		result.LeaseOwner = ""
		result.LeaseExpiresAt = now
		result.UpdatedAt = now
		if err = tx.Save(&result).Error; err != nil {
			return err
		}
		status := "succeeded"
		message := "交付对账已完成，请查看报告"
		if result.State == "failed" {
			status = "failed"
			message = "交付对账未完成，请查看报告中的处理提示"
		}
		if err = tx.Model(&Operation{}).Where("status IN ('accepted','unknown') AND response->>'reconciliation_id'=?", report.ID).Updates(map[string]interface{}{"status": status, "response": encode(map[string]interface{}{"run_id": report.RunID, "reconciliation_id": report.ID, "state": result.State, "code": result.ErrorCode, "message": message}), "updated_at": now}).Error; err != nil {
			return err
		}
		return s.audit(tx, identity.Principal{UserID: latest.OwnerID}, "reconcile_etl_delivery", latest.ID, result.State, "")
	})
	if finalErr != nil && parent.Err() == nil {
		slog.Error("Delivery reconciliation completion unavailable", "run_id", report.RunID, "code", etlFailureCode(finalErr))
	}
}
func (s *Server) StartETLReconciliationWorker(parent context.Context) func() {
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
			}
			select {
			case s.work <- struct{}{}:
			case <-ctx.Done():
				return
			}
			report, token, ok, e := s.claimETLReconciliation(ctx)
			if e != nil && ctx.Err() == nil {
				slog.Error("Delivery reconciliation queue unavailable")
			}
			if ok {
				s.reconcileETLDelivery(ctx, report, token)
			}
			<-s.work
		}
	}()
	return func() { cancel(); wg.Wait() }
}
