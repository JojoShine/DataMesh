package workflow

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) streamSave(ctx context.Context, row GovernanceStream, extra map[string]interface{}) error {
	values := map[string]interface{}{"state": row.State, "job_id": row.JobID, "session_id": row.SessionID, "operation_id": row.OperationID, "submission_started": row.SubmissionStarted, "stop_trigger": row.StopTrigger, "savepoint": row.Savepoint, "error_code": row.ErrorCode, "positions": row.Positions, "input_events": row.InputEvents, "output_rows": row.OutputRows, "rejected_rows": row.RejectedRows, "output_offset": row.OutputOffset, "caught_up": row.CaughtUp, "updated_at": time.Now()}
	for k, v := range extra {
		values[k] = v
	}
	result := s.db.WithContext(ctx).Model(&GovernanceStream{}).Where("id=? AND generation=? AND lease_owner=? AND lease_expires_at>CURRENT_TIMESTAMP", row.ID, row.Generation, row.LeaseOwner).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return problem(409, "STREAM_LEASE_LOST", "持续任务控制权已变化")
	}
	return nil
}
func (s *Server) streamTick(parent context.Context) error {
	row := GovernanceStream{}
	token, e := newID()
	if e != nil {
		return e
	}
	e = s.db.WithContext(parent).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(state NOT IN ('paused','failed') OR (desired_state='failed' AND state<>'failed')) AND lease_expires_at<CURRENT_TIMESTAMP").Order("updated_at").Take(&row).Error; e != nil {
			return e
		}
		row.LeaseOwner = token
		row.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		return tx.Save(&row).Error
	})
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	def, e := decode[governanceFlowExecution](row.Definition)
	if row.DesiredState == "failed" {
		e = s.streamConfirmTermination(ctx, &row)
	} else {
		if e == nil {
			_, e = s.governanceStreamAuthorize(ctx, row, def)
		}
		if e == nil {
			e = s.streamStep(ctx, &row, def)
		}
		if e == nil && row.State != "failed" {
			row.ErrorCode = ""
		}
		if e != nil {
			row.ErrorCode = "STREAM_RUNTIME_UNAVAILABLE"
			var p *Problem
			permanent := errors.Is(e, gorm.ErrRecordNotFound)
			if errors.As(e, &p) {
				row.ErrorCode = p.Code
				permanent = p.Status < 500 && p.Code != "STREAM_SUBMISSION_UNKNOWN" && p.Code != "STREAM_LEASE_LOST"
			}
			if permanent {
				row.DesiredState = "failed"
				if cancelErr := s.streamConfirmTermination(ctx, &row); cancelErr != nil {
					row.State = "unknown"
				}
			} else if row.SubmissionStarted {
				row.State = "unknown"
			}
		}
	}
	if err := s.streamFinish(ctx, row); err != nil {
		return err
	}
	return e
}

func (s *Server) streamFinish(ctx context.Context, row GovernanceStream) error {
	if ctx.Err() != nil {
		// Runtime cancellation must not strand the durable control lease. Keep
		// uncertain engine work reconcilable without claiming it has terminated.
		if row.SubmissionStarted && row.State != "paused" && row.State != "failed" {
			row.State = "unknown"
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	extra := map[string]interface{}{"lease_owner": "", "lease_expires_at": time.Unix(0, 0).UTC()}
	if row.State == "failed" || row.DesiredState == "failed" {
		extra["desired_state"] = "failed"
	}
	return s.streamSave(ctx, row, extra)
}
func (s *Server) streamStep(ctx context.Context, row *GovernanceStream, def governanceFlowExecution) (err error) {
	sources, e := decode[map[string]governanceStreamSource](row.Sources)
	if e != nil {
		return e
	}
	f := s.flink()
	name := streamJobName(row.ID, row.Generation)
	if row.State == "starting" || row.State == "unknown" {
		jobs, e := f.jobs(ctx, name)
		if e != nil {
			return e
		}
		if len(jobs) > 1 {
			return problem(409, "STREAM_MULTIPLE_JOBS", "发现重复作业，需要核对后再恢复")
		}
		if len(jobs) == 1 {
			row.JobID = jobs[0].ID
			// Job discovery resolves an uncertain submission without resubmitting it.
			if row.SessionID != "" {
				if e := f.closeSession(ctx, row.SessionID); e != nil {
					return e
				}
				row.SessionID, row.OperationID = "", ""
			}
			switch jobs[0].State {
			case "RUNNING":
				row.State = "running"
			case "FINISHED":
				if row.StopTrigger != "" {
					row.State = "pausing"
				} else {
					row.State = "failed"
					return nil
				}
			case "CANCELED", "FAILED":
				row.State = "failed"
				return nil
			default:
				return nil
			}
		} else if row.SubmissionStarted {
			return problem(409, "STREAM_SUBMISSION_UNKNOWN", "提交结果待核对，平台不会自动重复启动作业")
		}
		if row.State == "starting" {
			if row.SessionID != "" {
				if e = f.closeSession(ctx, row.SessionID); e != nil {
					return e
				}
				row.SessionID, row.OperationID = "", ""
			}
			if row.DesiredState == "paused" {
				row.State = "failed"
				return nil
			}
			if e = s.streamPrepare(ctx, *row, def, sources); e != nil {
				return e
			}
			row.SessionID, e = f.session(ctx, name, row.Savepoint)
			if e != nil {
				return e
			}
			session := row.SessionID
			submissionFinished := false
			defer func() {
				// Do not interrupt a submission whose cluster outcome is still unknown.
				// Detached jobs survive closing their planner session after confirmation.
				if !row.SubmissionStarted || submissionFinished {
					closeErr := f.closeSession(ctx, session)
					if closeErr == nil {
						row.SessionID, row.OperationID = "", ""
					}
					err = errors.Join(err, closeErr)
				}
			}()
			if e = f.statement(ctx, row.SessionID, "ADD JAR 'file:///opt/flink/usrlib/flink-sql-connector-kafka-3.4.0-1.20.jar'"); e != nil {
				return e
			}
			topics := map[string]string{}
			for node := range sources {
				topics[node] = streamTopic(row.ID, node)
			}
			compiled, e := compileGovernanceFlink(def, topics, streamOutputTopic(row.ID), name)
			if e != nil {
				return e
			}
			for _, statement := range compiled.Setup {
				if e = f.statement(ctx, row.SessionID, statement); e != nil {
					return e
				}
			}
			row.SubmissionStarted = true
			if e = s.streamSave(ctx, *row, nil); e != nil {
				return e
			}
			row.OperationID, e = f.submit(ctx, row.SessionID, compiled.Insert)
			if e != nil {
				return e
			}
			if e = s.streamSave(ctx, *row, nil); e != nil {
				return e
			}
			if e = f.wait(ctx, row.SessionID, row.OperationID); e != nil {
				return e
			}
			submissionFinished = true
			return nil
		}
	}
	if row.State == "running" || row.State == "pausing" {
		job, e := f.job(ctx, row.JobID)
		if e != nil {
			return e
		}
		if row.StopTrigger != "" {
			location, done, e := f.savepoint(ctx, row.JobID, row.StopTrigger)
			if e != nil {
				var p *Problem
				if errors.As(e, &p) && p.Code == "STREAM_TRIGGER_NOT_FOUND" && job.State == "RUNNING" {
					_, e = f.stop(ctx, row.JobID, row.StopTrigger)
				}
				return e
			}
			if !done {
				return nil
			}
			row.Savepoint = location
			if job.State != "FINISHED" {
				return nil
			}
			drained, e := s.streamMaterialize(ctx, row, def, true)
			if e != nil {
				return e
			}
			if drained {
				row.State = "paused"
				row.CaughtUp = true
			}
			return nil
		}
		if job.State != "RUNNING" {
			if job.State == "FAILED" || job.State == "CANCELED" || job.State == "FINISHED" {
				row.State = "failed"
				row.ErrorCode = "STREAM_JOB_TERMINATED"
			}
			return nil
		}
		if row.DesiredState == "paused" {
			row.State = "pausing"
			row.StopTrigger, e = newID()
			if e != nil {
				return e
			}
			if e = s.streamSave(ctx, *row, nil); e != nil {
				return e
			}
			_, e = f.stop(ctx, row.JobID, row.StopTrigger)
			return e
		}
		if e = s.streamForward(ctx, row, sources); e != nil {
			return e
		}
		_, e = s.streamMaterialize(ctx, row, def, false)
		return e
	}
	return nil
}
func (s *Server) streamPrepare(ctx context.Context, row GovernanceStream, def governanceFlowExecution, sources map[string]governanceStreamSource) error {
	if _, _, _, e := governanceExecutionPlan(def); e != nil {
		return e
	}
	if !dorisObjectID.MatchString(row.ID) || !validDataLayer(streamResultLayer(def)) {
		return invalid
	}
	for node := range sources {
		if e := streamCreateTopic(ctx, streamTopic(row.ID, node)); e != nil {
			return e
		}
	}
	if e := streamCreateTopic(ctx, streamOutputTopic(row.ID)); e != nil {
		return e
	}
	db, e := dorisOpen(ctx, true)
	if e != nil {
		return e
	}
	defer db.Close()
	_, e = db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+dorisDatabase("ods")+".live_"+row.ID+" (node_id VARCHAR(128) NOT NULL,row_key VARCHAR(128) NOT NULL,source_offset BIGINT NOT NULL,deleted BOOLEAN NOT NULL,payload JSON NOT NULL) UNIQUE KEY(node_id,row_key) DISTRIBUTED BY HASH(row_key) BUCKETS 1 PROPERTIES ('replication_num'='1','enable_unique_key_merge_on_write'='true','function_column.sequence_col'='source_offset')")
	if e != nil {
		return e
	}
	for _, table := range []string{streamResultTable(def, row.ID), dorisDatabase("quality") + ".live_" + row.ID} {
		_, e = db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (node_id VARCHAR(128) NOT NULL,port VARCHAR(32) NOT NULL,row_key VARCHAR(128) NOT NULL,source_offset BIGINT NOT NULL,deleted BOOLEAN NOT NULL,payload JSON NOT NULL,violations JSON NOT NULL) UNIQUE KEY(node_id,port,row_key) DISTRIBUTED BY HASH(row_key) BUCKETS 1 PROPERTIES ('replication_num'='1','enable_unique_key_merge_on_write'='true','function_column.sequence_col'='source_offset')")
		if e != nil {
			return e
		}
	}
	return e
}
func (s *Server) runStreamWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e := s.streamTick(ctx); e != nil && ctx.Err() == nil {
				slog.Warn("continuous governance reconciliation pending")
			}
		}
	}
}

// A revoked contract must stop a job even when the original actor no longer
// passes authorization. Never release its slot before Flink confirms termination.
func (s *Server) streamConfirmTermination(ctx context.Context, row *GovernanceStream) error {
	if !row.SubmissionStarted {
		// Failed rows leave the reconciliation queue, so release owned resources first.
		if row.SessionID != "" {
			if err := s.flink().closeSession(ctx, row.SessionID); err != nil {
				return err
			}
			row.SessionID, row.OperationID = "", ""
		}
		row.State = "failed"
		return nil
	}
	jobs, e := s.flink().jobs(ctx, streamJobName(row.ID, row.Generation))
	if e != nil {
		return e
	}
	if len(jobs) == 0 {
		row.State = "unknown"
		return problem(409, "STREAM_SUBMISSION_UNKNOWN", "提交结果仍待核对")
	}
	terminal := true
	for _, job := range jobs {
		switch job.State {
		case "FAILED", "CANCELED", "FINISHED":
		default:
			terminal = false
			if e = s.flink().cancel(ctx, job.ID); e != nil {
				return e
			}
		}
	}
	if terminal {
		if row.SessionID != "" {
			if err := s.flink().closeSession(ctx, row.SessionID); err != nil {
				return err
			}
			row.SessionID, row.OperationID = "", ""
		}
		row.State = "failed"
	} else {
		row.State = "unknown"
	}
	return nil
}
