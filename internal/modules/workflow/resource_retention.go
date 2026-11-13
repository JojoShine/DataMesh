package workflow

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// References retain immutable inputs throughout active runs and a day after completion.
func (s *Server) sweepResourceStorage(ctx context.Context) error {
	if e := s.sweepDeliveryIndices(ctx); e != nil {
		return e
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	if e := s.db.WithContext(ctx).Exec(`DELETE FROM resource_snapshot_refs ref USING business_records run WHERE run.kind='etl_run' AND run.id=ref.run_id AND run.state IN ('succeeded','failed','cancelled','interrupted') AND run.updated_at<?`, cutoff).Error; e != nil {
		return e
	}
	if e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if e := tx.Model(&ResourceSnapshot{}).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("updated_at<? AND lease_expires_at<CURRENT_TIMESTAMP AND NOT EXISTS(SELECT 1 FROM resource_snapshot_refs ref WHERE ref.snapshot_id=resource_snapshots.id) AND NOT EXISTS(SELECT 1 FROM business_records d WHERE d.kind='dataset' AND (d.body->'result_binding'->>'result_snapshot_id'=resource_snapshots.id OR d.body->'result_binding'->>'input_snapshot_id'=resource_snapshots.id))", cutoff).Limit(20).Pluck("id", &ids).Error; e != nil {
			return e
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Where("id IN ?", ids).Delete(&ResourceSnapshot{}).Error
	}); e != nil {
		return e
	}
	if s.runtime.store == nil {
		return nil
	}
	var objects []archiveStoredObject
	if e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='deleting' OR (state='staging' AND updated_at<?)", cutoff).Limit(100).Find(&objects).Error; e != nil {
			return e
		}
		keys := []string{}
		for _, obj := range objects {
			keys = append(keys, obj.Key)
		}
		if len(keys) == 0 {
			return nil
		}
		return tx.Model(&archiveStoredObject{}).Where("key IN ?", keys).Update("state", "deleting").Error
	}); e != nil {
		return e
	}
	for _, obj := range objects {
		if e := s.runtime.store.RemoveObject(ctx, "datamesh-raw", obj.Key, minio.RemoveObjectOptions{}); e != nil {
			return e
		}
		if e := s.db.WithContext(ctx).Where("key=? AND state='deleting'", obj.Key).Delete(&archiveStoredObject{}).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) StartResourceRetentionWorker(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweep, done := context.WithTimeout(ctx, time.Minute)
		if e := s.sweepResourceStorage(sweep); e != nil && ctx.Err() == nil {
			slog.Error("Resource storage retention failed", "code", captureErrorCode(e))
		}
		done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep, done := context.WithTimeout(ctx, time.Minute)
				if e := s.sweepResourceStorage(sweep); e != nil && ctx.Err() == nil {
					slog.Error("Resource storage retention failed", "code", captureErrorCode(e))
				}
				done()
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func (s *Server) sweepDeliveryIndices(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runs []string
		if e := tx.Model(&etlDeliveryInput{}).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state<>'expired' AND expires_at<CURRENT_TIMESTAMP AND NOT EXISTS(SELECT 1 FROM business_records run WHERE run.kind='etl_run' AND run.id=etl_delivery_inputs.run_id AND run.state IN ('queued','extracting','loading','committing','unknown')) AND NOT EXISTS(SELECT 1 FROM etl_reconciliations r WHERE r.run_id=etl_delivery_inputs.run_id AND r.state IN ('pending','checking'))").Limit(20).Pluck("run_id", &runs).Error; e != nil {
			return e
		}
		if len(runs) == 0 {
			return nil
		}
		if e := tx.Where("run_id IN ?", runs).Delete(&etlDeliveryKey{}).Error; e != nil {
			return e
		}
		return tx.Model(&etlDeliveryInput{}).Where("run_id IN ?", runs).Updates(map[string]interface{}{"state": "expired", "wrapped_key": []byte{}, "updated_at": time.Now()}).Error
	})
}
