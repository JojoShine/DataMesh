package workflow

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) claimGovernedCheck(ctx context.Context) (GovernedCheck, string, bool, error) {
	var job GovernedCheck
	token, e := newID()
	if e != nil {
		return job, "", false, e
	}
	claimed := false
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []GovernedCheck
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='queued' OR (state='checking' AND lease_expires_at<CURRENT_TIMESTAMP)").Order("created_at").Limit(1).Find(&jobs).Error; e != nil {
			return e
		}
		if len(jobs) == 0 {
			return nil
		}
		job = jobs[0]
		job.State = "checking"
		job.LeaseOwner = token
		job.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		job.ErrorCode = ""
		job.Processed = 0
		if e := tx.Save(&job).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	return job, token, claimed, e
}
func (s *Server) executeGovernedCheck(parent context.Context, job GovernedCheck, token string) {
	ctx, cancel := context.WithTimeout(parent, s.etl.Timeout)
	defer cancel()
	done := make(chan struct{})
	var monitor sync.WaitGroup
	monitor.Add(1)
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewed := s.db.WithContext(ctx).Model(&GovernedCheck{}).Where("id=? AND lease_owner=? AND state='checking' AND lease_expires_at>CURRENT_TIMESTAMP", job.ID, token).Updates(map[string]interface{}{"lease_expires_at": time.Now().Add(90 * time.Second), "updated_at": time.Now()})
				if renewed.Error != nil || renewed.RowsAffected != 1 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(done); monitor.Wait() }()
	input, e := decode[GovernedBindingInput](job.Definition)
	p, principalErr := s.etlPrincipal(ctx, job.ActorID)
	if e == nil {
		e = principalErr
	}
	var data DatasetData
	var task Record
	var object Object
	authorize := func() error {
		if err := s.etlInputIdentity(ctx, p); err != nil {
			return err
		}
		live, err := s.etlPrincipal(ctx, job.ActorID)
		if err != nil {
			return err
		}
		dataset, err := get(s.db.WithContext(ctx), "dataset", job.DatasetID, false)
		if err != nil {
			return err
		}
		_, _, _, err = s.governedBindingBasis(ctx, s.db.WithContext(ctx), live, dataset, input)
		if err != nil {
			return err
		}
		source, err := get(s.db.WithContext(ctx), "source", input.SourceID, false)
		if err != nil {
			return err
		}
		if source.Version != input.SourceVersion {
			return problem(409, "SOURCE_VERSION_CHANGED", "结果数据源版本已变化")
		}
		if err = requireResourceCapability(s.db.WithContext(ctx), live, Resource{Type: "source", ID: source.ID}, "operate_resource"); err != nil {
			return err
		}
		src, err := decode[SourceSecret](source.Body)
		if err != nil {
			return err
		}
		return s.runtime.allowSource(ctx, s.db, SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database})
	}
	if e == nil {
		var dataset Record
		dataset, e = get(s.db.WithContext(ctx), "dataset", job.DatasetID, false)
		if e == nil {
			data, task, object, e = s.governedBindingBasis(ctx, s.db.WithContext(ctx), p, dataset, input)
		}
	}
	inputID := ""
	quality := Quality{DatasetID: job.DatasetID, ContractVersion: input.ContractVersion, RuleVersion: input.RuleVersion, Status: "blocked", FailureReasons: []Failure{}, CheckedRange: encode(map[string]interface{}{})}
	if e == nil {
		var archive Record
		archive, e = get(s.db.WithContext(ctx), "archive", input.InputArchiveID, false)
		if e == nil {
			var meta ArchiveData
			meta, e = decode[ArchiveData](archive.Body)
			if e == nil {
				inputID, e = s.buildRawSnapshot(ctx, task, meta, authorize)
			}
		}
	}
	if e == nil {
		quality, e = s.buildGovernedResult(ctx, job, token, p, input, data, object, inputID, authorize)
	}
	if e == nil {
		e = authorize()
	}
	finish, doneFinish := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer doneFinish()
	if parent.Err() != nil {
		if err := s.db.WithContext(finish).Model(&GovernedCheck{}).Where("id=? AND lease_owner=? AND state='checking'", job.ID, token).Updates(map[string]interface{}{"state": "queued", "lease_owner": "", "lease_expires_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
			slog.Error("Governed check requeue unavailable", "check_id", job.ID)
		}
		return
	}
	finalErr := s.db.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		if err := s.governedCheckFence(tx, job.ID, token); err != nil {
			return err
		}
		dataset, err := get(tx, "dataset", job.DatasetID, true)
		if err != nil {
			return err
		}
		current, err := decode[DatasetData](dataset.Body)
		if err != nil {
			return err
		}
		if current.RuleVersion != input.RuleVersion || dataset.Version != input.ContractVersion {
			e = problem(409, "RESULT_VERSION_CHANGED", "核验期间契约或规则已变化")
		}
		state := "passed"
		snapshotState := "ready"
		status := "succeeded"
		message := "治理结果已完成完整范围质量与来源核对"
		if e != nil {
			state = "blocked"
			snapshotState = "failed"
			status = "failed"
			message = "治理结果核验未通过，请查看检查编号对应的原因"
			quality.Status = "blocked"
			if len(quality.FailureReasons) == 0 {
				quality.FailureReasons = []Failure{{Code: captureErrorCode(e), Count: 1}}
			}
		}
		if err = tx.Model(&ResourceSnapshot{}).Where("id=? AND lease_owner=?", job.ResultSnapshotID, token).Updates(map[string]interface{}{"state": snapshotState, "lease_owner": "", "lease_expires_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if e == nil {
			current.ResultBinding = &GovernedResultBinding{GovernedBindingInput: input, DatasetID: job.DatasetID, InputSnapshotID: inputID, ResultSnapshotID: job.ResultSnapshotID}
			current.CompletenessStatus = "complete"
			current.LatestWatermark = quality.CheckedRange
		} else {
			current.ResultBinding = nil
			current.CompletenessStatus = "pending"
		}
		current.QualityStatus = quality.Status
		current.Quality = encode(quality)
		dataset.Body = encode(current)
		if err = save(tx, &dataset); err != nil {
			return err
		}
		if err = tx.Model(&GovernedCheck{}).Where("id=? AND lease_owner=?", job.ID, token).Updates(map[string]interface{}{"state": state, "error_code": captureErrorCode(e), "lease_owner": "", "lease_expires_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err = tx.Model(&Operation{}).Where("status IN ('accepted','unknown') AND response->>'governed_check_id'=?", job.ID).Updates(map[string]interface{}{"status": status, "response": encode(map[string]interface{}{"governed_check_id": job.ID, "dataset_id": job.DatasetID, "code": captureErrorCode(e), "message": message}), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return s.audit(tx, identity.Principal{UserID: job.ActorID}, "check_governed_binding", job.DatasetID, state, "")
	})
	if finalErr != nil && parent.Err() == nil {
		slog.Error("Governed result check completion unavailable", "check_id", job.ID, "code", captureErrorCode(finalErr))
	}
}
func (s *Server) StartGovernedWorkers(parent context.Context) func() {
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
			job, token, ok, e := s.claimGovernedCheck(ctx)
			if e != nil && ctx.Err() == nil {
				slog.Error("Governed result queue unavailable")
			}
			if ok {
				s.executeGovernedCheck(ctx, job, token)
			}
			<-s.work
		}
	}()
	return func() { cancel(); wg.Wait() }
}
