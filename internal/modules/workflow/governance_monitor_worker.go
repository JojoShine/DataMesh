package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math"
	"time"
)

type MonitorFieldMetric struct {
	Name               string   `json:"name"`
	NullRate           float64  `json:"null_rate"`
	DuplicateRate      *float64 `json:"duplicate_rate"`
	DistributionChange *float64 `json:"distribution_change"`
	DistributionStatus string   `json:"distribution_status"`
}
type MonitorReport struct {
	Rows            int64                `json:"rows"`
	VolumeChange    *float64             `json:"volume_change"`
	FreshnessStatus string               `json:"freshness_status"`
	BusinessTime    *time.Time           `json:"business_time"`
	Fields          []MonitorFieldMetric `json:"fields"`
	Violations      []string             `json:"violations"`
	ObservedAt      time.Time            `json:"observed_at"`
	Baseline        bool                 `json:"baseline"`
}

func monitorDistribution(a, b ProfileField, an, bn int64) (*float64, string) {
	if a.Redacted || b.Redacted {
		return nil, "redacted"
	}
	if an == 0 || bn == 0 {
		return nil, "empty"
	}
	counts := map[string]float64{}
	var aCount, bCount int64
	for _, f := range a.Frequencies {
		if f.Truncated {
			return nil, "incomplete"
		}
		aCount += f.Count
		counts[f.Value] += float64(f.Count) / float64(an)
	}
	for _, f := range b.Frequencies {
		if f.Truncated {
			return nil, "incomplete"
		}
		bCount += f.Count
		counts[f.Value] -= float64(f.Count) / float64(bn)
	}
	if a.ValuesTruncated || b.ValuesTruncated || aCount != an-a.NullCount || bCount != bn-b.NullCount {
		return nil, "incomplete"
	}
	distance := math.Abs(float64(a.NullCount)/float64(an) - float64(b.NullCount)/float64(bn))
	for _, delta := range counts {
		distance += math.Abs(delta)
	}
	distance /= 2
	return &distance, "complete"
}
func evaluateMonitor(policy GovernanceMonitorPolicy, current ProfileReport, baseline *ProfileReport, now time.Time) MonitorReport {
	out := MonitorReport{Rows: current.TotalRows, FreshnessStatus: "not_configured", Fields: []MonitorFieldMetric{}, Violations: []string{}, ObservedAt: current.ObservedAt, Baseline: baseline == nil}
	if baseline != nil {
		if baseline.TotalRows == 0 {
			if current.TotalRows == 0 {
				zero := float64(0)
				out.VolumeChange = &zero
			} else {
				out.Violations = append(out.Violations, "volume_from_empty")
			}
		} else {
			delta := math.Abs(float64(current.TotalRows-baseline.TotalRows)) / float64(baseline.TotalRows)
			out.VolumeChange = &delta
			if delta > policy.MaxVolumeChange {
				out.Violations = append(out.Violations, "volume_changed")
			}
		}
	}
	if policy.FreshnessSeconds > 0 {
		out.FreshnessStatus = "unknown"
	}
	for _, field := range current.Fields {
		result := MonitorFieldMetric{Name: field.Name, DistributionStatus: "no_baseline"}
		if current.TotalRows > 0 {
			result.NullRate = float64(field.NullCount) / float64(current.TotalRows)
			if field.DuplicateCount != nil {
				rate := float64(*field.DuplicateCount) / float64(current.TotalRows)
				result.DuplicateRate = &rate
				if rate > policy.MaxDuplicateRate {
					out.Violations = append(out.Violations, "duplicates:"+field.Name)
				}
			}
		}
		if result.NullRate > policy.MaxNullRate {
			out.Violations = append(out.Violations, "nulls:"+field.Name)
		}
		if baseline != nil {
			for _, old := range baseline.Fields {
				if old.Name == field.Name {
					result.DistributionChange, result.DistributionStatus = monitorDistribution(field, old, current.TotalRows, baseline.TotalRows)
					if result.DistributionChange != nil && *result.DistributionChange > policy.MaxDistributionChange {
						out.Violations = append(out.Violations, "distribution:"+field.Name)
					}
				}
			}
		}
		if policy.FreshnessSeconds > 0 && field.Name == policy.TimeField && current.BusinessTimeStatus == "verified" && current.BusinessTime != nil {
			timestamp := *current.BusinessTime
			out.BusinessTime = &timestamp
			if timestamp.After(now.Add(5 * time.Minute)) {
				out.FreshnessStatus = "future"
				out.Violations = append(out.Violations, "business_time_future")
			} else if now.Sub(timestamp) > time.Duration(policy.FreshnessSeconds)*time.Second {
				out.FreshnessStatus = "stale"
				out.Violations = append(out.Violations, "freshness")
			} else {
				out.FreshnessStatus = "fresh"
			}
		}
		out.Fields = append(out.Fields, result)
	}
	if out.FreshnessStatus == "unknown" {
		out.Violations = append(out.Violations, "freshness_unknown")
	}
	return out
}
func (s *Server) sweepGovernanceMonitors(ctx context.Context, runIDs ...string) error {
	rows := []GovernanceMonitor{}
	q := s.db.WithContext(ctx).Where("pending_run_id IS NOT NULL OR (enabled AND next_at<=now())")
	if len(runIDs) > 0 {
		q = q.Where("pending_run_id IN ?", runIDs)
	}
	if e := q.Order("next_at,id").Limit(20).Find(&rows).Error; e != nil {
		return e
	}
	for _, candidate := range rows {
		e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row GovernanceMonitor
			e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("id=? AND version=?", candidate.ID, candidate.Version).Take(&row).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			scoped := workspaceContext(tx, row.WorkspaceID, false)
			p, auth := s.etlPrincipal(ctx, row.ActorID)
			if auth == nil && (!role(p, "governor") || !platform(p, "governance")) {
				auth = denied
			}
			if auth == nil {
				_, auth = governanceWorkspaceWrite(scoped, p, row.WorkspaceID)
			}
			cfg, e := decode[GovernanceMonitorPolicy](row.Policy)
			if e != nil {
				return e
			}
			if auth == nil {
				_, auth = s.governanceInputBasisParameters(scoped, p, cfg.Resource, cfg.Fields, false, cfg.Parameters)
			}
			now := time.Now().UTC()
			if row.PendingRunID == nil {
				if !row.Enabled || row.NextAt.After(now) {
					return nil
				}
				if auth == nil {
					auth = scoped.Transaction(func(inner *gorm.DB) error { _, e := s.monitorQueue(inner, p, &row); return e })
				}
				if auth == nil {
					return nil
				}
				id, e := newID()
				if e != nil {
					return e
				}
				out := GovernanceMonitorObservation{ID: id, MonitorID: row.ID, PolicyVersion: row.Version, Policy: row.Policy, State: "failed", Report: json.RawMessage(`{}`), ErrorCode: captureErrorCode(auth), FinishedAt: &now}
				if e = tx.Create(&out).Error; e != nil {
					return e
				}
				row.Enabled = false
				row.LastObservationID = &out.ID
				return tx.Save(&row).Error
			}
			var run GovernanceRun
			if e = tx.Where("id=?", *row.PendingRunID).Take(&run).Error; e != nil {
				return e
			}
			if run.State == "queued" || run.State == "running" {
				return nil
			}
			var out GovernanceMonitorObservation
			if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("monitor_id=? AND run_id=? AND state='queued'", row.ID, run.ID).Take(&out).Error; e != nil {
				return e
			}
			out.FinishedAt = &now
			switch {
			case auth != nil:
				out.State = "failed"
				out.ErrorCode = captureErrorCode(auth)
				row.Enabled = false
			case out.PolicyVersion != row.Version:
				out.State = "superseded"
				out.ErrorCode = "MONITOR_POLICY_CHANGED"
			case run.State != "succeeded":
				out.State = "failed"
				out.ErrorCode = run.ErrorCode
				if out.ErrorCode == "" {
					out.ErrorCode = "MONITOR_PROFILE_FAILED"
				}
			default:
				var current ProfileReport
				if e = json.Unmarshal(run.Result, &current); e != nil {
					return e
				}
				if current.Coverage != "full" || current.Rows != current.TotalRows {
					out.State = "failed"
					out.ErrorCode = "MONITOR_FULL_COVERAGE_REQUIRED"
					break
				}
				var baseline *ProfileReport
				if row.BaselineID != nil {
					var old GovernanceMonitorObservation
					if e = tx.Where("id=? AND monitor_id=?", *row.BaselineID, row.ID).Take(&old).Error; e != nil {
						return e
					}
					if old.RunID != nil {
						var previous GovernanceRun
						if e = tx.Where("id=?", *old.RunID).Take(&previous).Error; e != nil {
							return e
						}
						var report ProfileReport
						if e = json.Unmarshal(previous.Result, &report); e != nil {
							return e
						}
						baseline = &report
					}
				}
				result := evaluateMonitor(cfg, current, baseline, now)
				out.State = "passed"
				if len(result.Violations) > 0 {
					out.State = "alert"
				}
				out.Report = encode(result)
				if row.BaselineID == nil {
					row.BaselineID = &out.ID
				}
			}
			if e = tx.Save(&out).Error; e != nil {
				return e
			}
			row.PendingRunID = nil
			row.LastObservationID = &out.ID
			return tx.Save(&row).Error
		})
		if e != nil {
			return e
		}
	}
	return nil
}
