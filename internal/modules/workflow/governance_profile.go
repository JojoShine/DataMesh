package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

type governanceProfileInput struct {
	Parameters map[string]string `json:"parameters,omitempty"`
	Resource   Resource          `json:"resource"`
	Fields     []string          `json:"fields"`
	Mode       string            `json:"mode"`
	SampleRows int               `json:"sample_rows"`
}
type governanceProfileDefinition struct {
	BusinessTimeField string                      `json:"business_time_field,omitempty"`
	StandardBindings  []GovernanceStandardBinding `json:"standard_bindings,omitempty"`
	Input             governanceInput             `json:"input"`
	Mode              string                      `json:"mode"`
	SampleRows        int                         `json:"sample_rows"`
}
type ProfileFrequency struct {
	Value     string `json:"value"`
	Count     int64  `json:"count"`
	Truncated bool   `json:"truncated"`
}
type ProfileField struct {
	Name            string             `json:"name"`
	LogicalType     string             `json:"logical_type"`
	NullCount       int64              `json:"null_count"`
	DistinctCount   *int64             `json:"distinct_count"`
	DuplicateCount  *int64             `json:"duplicate_count"`
	Min             *string            `json:"min"`
	Max             *string            `json:"max"`
	Redacted        bool               `json:"redacted"`
	ValuesTruncated bool               `json:"values_truncated"`
	Frequencies     []ProfileFrequency `json:"frequencies"`
}
type ProfileAnomaly struct {
	Ordinal int64  `json:"ordinal"`
	Field   string `json:"field"`
	Code    string `json:"code"`
}
type ProfileReport struct {
	BusinessTime       *time.Time               `json:"business_time,omitempty"`
	BusinessTimeStatus string                   `json:"business_time_status,omitempty"`
	StandardAudit      *GovernanceStandardAudit `json:"standard_audit,omitempty"`
	Mode               string                   `json:"mode"`
	Coverage           string                   `json:"coverage"`
	Sampling           string                   `json:"sampling"`
	Rows               int64                    `json:"rows"`
	TotalRows          int64                    `json:"total_rows"`
	Fields             []ProfileField           `json:"fields"`
	Anomalies          []ProfileAnomaly         `json:"anomalies"`
	SnapshotKind       string                   `json:"snapshot_kind"`
	ObservedAt         time.Time                `json:"observed_at"`
}

func (s *Server) governanceCreateProfile(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	input, err := bind[governanceProfileInput](b)
	if err != nil {
		return 0, nil, "", err
	}
	return s.enqueueGovernanceProfile(tx, p, input, nil)
}
func (s *Server) enqueueGovernanceProfile(tx *gorm.DB, p identity.Principal, input governanceProfileInput, retryOf *string) (int, json.RawMessage, string, error) {
	space, spaceErr := governanceWorkspaceCreateScope(tx, p, p.DepartmentID)
	if spaceErr != nil {
		return 0, nil, "", spaceErr
	}

	if !slices.Contains([]string{"full", "sample"}, input.Mode) || (input.Mode == "full" && input.SampleRows != 0) || input.SampleRows < 0 || input.SampleRows > 10000 {
		return 0, nil, "", invalid
	}
	if input.Mode == "sample" && input.SampleRows == 0 {
		input.SampleRows = 1000
	}
	if err := governanceWorkspaceReference(tx, p, space.ID, input.Resource); err != nil {
		return 0, nil, "", err
	}
	basis, err := s.governanceInputBasisParameters(tx, p, input.Resource, input.Fields, false, input.Parameters)
	if err != nil {
		return 0, nil, "", err
	}
	// Serialize queue limits with the worker capacity check across API replicas.
	if err = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; err != nil {
		return 0, nil, "", err
	}
	var pending int64
	if err = tx.Model(&GovernanceRun{}).Where("actor_id=? AND state IN ('queued','running')", p.UserID).Count(&pending).Error; err != nil {
		return 0, nil, "", err
	}
	if pending >= 10 {
		return 0, nil, "", problem(409, "GOVERNANCE_QUEUE_FULL", "最多保留十个未结束治理任务，请先取消或等待完成")
	}
	id, err := newID()
	if err != nil {
		return 0, nil, "", err
	}
	run := GovernanceRun{ID: id, WorkspaceID: space.ID, Kind: "profile", DepartmentID: p.DepartmentID, ActorID: p.UserID, Definition: encode(governanceProfileDefinition{Input: basis, Mode: input.Mode, SampleRows: input.SampleRows}), State: "queued", RetryOf: retryOf, Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if err = tx.Create(&run).Error; err != nil {
		return 0, nil, "", err
	}
	if err = governanceRunEvent(tx, run, "PROFILE_QUEUED"); err != nil {
		return 0, nil, "", err
	}
	return 201, encode(run), run.ID, nil
}
func (s *Server) executeGovernanceProfile(ctx context.Context, p identity.Principal, run GovernanceRun, def governanceProfileDefinition, guard func() error) (json.RawMessage, int64, error) {
	limit := s.etl.MaxRows + 1
	if def.Mode == "sample" {
		limit = min(def.SampleRows, s.etl.MaxRows)
	}
	stage, total, err := s.governanceStageInput(ctx, p, def.Input, limit, guard, func(processed int64) error {
		result := s.db.WithContext(ctx).Model(&GovernanceRun{}).Where("id=? AND lease_owner=? AND state='running' AND lease_expires_at>CURRENT_TIMESTAMP AND cancel_requested=false", run.ID, run.LeaseOwner).Updates(map[string]interface{}{"processed": processed, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return problem(409, "GOVERNANCE_LEASE_LOST", "运行已取消或租约失效")
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	defer stage.tx.Rollback()
	report := ProfileReport{Mode: def.Mode, Coverage: "full", Rows: stage.count, TotalRows: total, Fields: []ProfileField{}, Anomalies: []ProfileAnomaly{}, SnapshotKind: "database_statement", ObservedAt: time.Now().UTC()}
	if stage.count < total {
		report.Coverage = "sample"
	}
	if def.Mode == "sample" {
		report.Sampling = "first_rows_not_random"
	}
	_, target, err := metadataTarget(s.db.WithContext(ctx), p, def.Input.Resource.ID, false)
	if err != nil {
		return nil, stage.count, err
	}
	switch def.Input.Resource.Type {
	case "file_resource":
		report.SnapshotKind = "verified_file_digest"
	case "api_resource":
		report.SnapshotKind = "single_bounded_api_response"
	case "governed_dataset":
		report.SnapshotKind = "immutable_governance_output"
	}
	if target.Adapter == "excel" {
		report.SnapshotKind = "immutable_import_batch"
	}
	if err = s.db.WithContext(ctx).Model(&GovernanceRun{}).Where("id=? AND lease_owner=? AND state='running' AND lease_expires_at>CURRENT_TIMESTAMP", run.ID, run.LeaseOwner).Update("processed", stage.count).Error; err != nil {
		return nil, stage.count, err
	}
	if def.BusinessTimeField != "" {
		for _, field := range def.Input.Fields {
			if field.Name != def.BusinessTimeField {
				continue
			}
			report.BusinessTimeStatus = "redacted"
			if !field.Sensitive {
				report.BusinessTime, report.BusinessTimeStatus, err = profileBusinessTime(ctx, stage.tx, field.Name, guard)
				if err != nil {
					return nil, stage.count, err
				}
			}
		}
	}
	for _, field := range def.Input.Fields {
		if err = guard(); err != nil {
			return nil, stage.count, err
		}
		result := ProfileField{Name: field.Name, LogicalType: field.LogicalType, Redacted: field.Sensitive, Frequencies: []ProfileFrequency{}}
		if err = stage.tx.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE payload->>$1 IS NULL) FROM governance_input_stage", field.Name).Scan(&result.NullCount); err != nil {
			return nil, stage.count, err
		}
		if !field.Sensitive {
			expression := "(payload->>$1) COLLATE \"C\""
			if field.LogicalType == "integer" || field.LogicalType == "decimal" {
				expression = "(payload->>$1)::numeric"
			}
			var distinct int64
			if err = stage.tx.QueryRowContext(ctx, "SELECT count(DISTINCT "+expression+") FROM governance_input_stage", field.Name).Scan(&distinct); err != nil {
				return nil, stage.count, err
			}
			duplicates := stage.count - result.NullCount - distinct
			result.DistinctCount = &distinct
			result.DuplicateCount = &duplicates
			var lo, hi sql.NullString
			if err = stage.tx.QueryRowContext(ctx, "SELECT min("+expression+")::text,max("+expression+")::text FROM governance_input_stage", field.Name).Scan(&lo, &hi); err != nil {
				return nil, stage.count, err
			}
			if lo.Valid {
				value, cut := profileShortValue(lo.String)
				result.Min = &value
				result.ValuesTruncated = result.ValuesTruncated || cut
			}
			if hi.Valid {
				value, cut := profileShortValue(hi.String)
				result.Max = &value
				result.ValuesTruncated = result.ValuesTruncated || cut
			}
			rows, e := stage.tx.QueryContext(ctx, "SELECT ("+expression+")::text,count(*) FROM governance_input_stage WHERE payload->>$1 IS NOT NULL GROUP BY "+expression+" ORDER BY count(*) DESC,"+expression+" LIMIT 10", field.Name)
			if e != nil {
				return nil, stage.count, e
			}
			for rows.Next() {
				var value string
				var count int64
				if e = rows.Scan(&value, &count); e != nil {
					rows.Close()
					return nil, stage.count, e
				}
				short, cut := profileShortValue(value)
				result.Frequencies = append(result.Frequencies, ProfileFrequency{short, count, cut})
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, stage.count, e
			}
		}
		if result.DuplicateCount != nil && *result.DuplicateCount > 0 && len(report.Anomalies) < 20 {
			expression := "(payload->>$1) COLLATE \"C\""
			if field.LogicalType == "integer" || field.LogicalType == "decimal" {
				expression = "(payload->>$1)::numeric"
			}
			rows, e := stage.tx.QueryContext(ctx, "SELECT ordinal FROM (SELECT ordinal,row_number() OVER(PARTITION BY "+expression+" ORDER BY ordinal) AS occurrence FROM governance_input_stage WHERE payload->>$1 IS NOT NULL) occurrences WHERE occurrence>1 ORDER BY ordinal LIMIT $2", field.Name, 20-len(report.Anomalies))
			if e != nil {
				return nil, stage.count, e
			}
			for rows.Next() {
				var ordinal int64
				if e = rows.Scan(&ordinal); e != nil {
					rows.Close()
					return nil, stage.count, e
				}
				report.Anomalies = append(report.Anomalies, ProfileAnomaly{ordinal, field.Name, "duplicate_value"})
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, stage.count, e
			}
		}
		report.Fields = append(report.Fields, result)
		if !field.Nullable && result.NullCount > 0 && len(report.Anomalies) < 20 {
			rows, e := stage.tx.QueryContext(ctx, "SELECT ordinal FROM governance_input_stage WHERE payload->>$1 IS NULL ORDER BY ordinal LIMIT $2", field.Name, 20-len(report.Anomalies))
			if e != nil {
				return nil, stage.count, e
			}
			for rows.Next() {
				var ordinal int64
				if e = rows.Scan(&ordinal); e != nil {
					rows.Close()
					return nil, stage.count, e
				}
				report.Anomalies = append(report.Anomalies, ProfileAnomaly{ordinal, field.Name, "required"})
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, stage.count, e
			}
		}
	}
	if err = guard(); err != nil {
		return nil, stage.count, err
	}
	report.StandardAudit, err = s.auditGovernanceStandards(ctx, p, def, stage.tx, guard)
	if err != nil {
		return nil, stage.count, err
	}
	return encode(report), stage.count, nil
}
func profileShortValue(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) > 128 {
		return string(runes[:128]) + "…", true
	}
	return value, false
}
