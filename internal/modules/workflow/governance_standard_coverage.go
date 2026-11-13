package workflow

import (
	"context"
	"database/sql"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"slices"
)

type GovernanceStandardBinding struct {
	Field       string        `json:"field"`
	Standard    GovernanceRef `json:"standard"`
	Fingerprint string        `json:"fingerprint"`
}
type GovernanceStandardAudit struct {
	Rows         int64                          `json:"rows"`
	RejectedRows int64                          `json:"rejected_rows"`
	Fields       []GovernanceStandardFieldAudit `json:"fields"`
}
type GovernanceStandardFieldAudit struct {
	Status     string        `json:"status"`
	Field      string        `json:"field"`
	Standard   GovernanceRef `json:"standard"`
	Name       string        `json:"name"`
	Violations int64         `json:"violations"`
	Ordinals   []int64       `json:"ordinals"`
}

func resolveStandardBindings(tx *gorm.DB, p identity.Principal, bindings []GovernanceStandardBinding) (map[GovernanceRef]GovernanceStandard, []GovernanceStandardFieldAudit, error) {
	standards := map[GovernanceRef]GovernanceStandard{}
	fields := []GovernanceStandardFieldAudit{}
	for _, binding := range bindings {
		_, version, e := governanceRead(tx, p, "standard", binding.Standard.ID, binding.Standard.Version)
		if e != nil {
			return nil, nil, e
		}
		if version.State != "published" || binding.Fingerprint != "" && fingerprint(version.Definition) != binding.Fingerprint {
			return nil, nil, problem(409, "GOVERNANCE_STANDARD_CHANGED", "固定的标准版本已失效，请重新确认")
		}
		definition, e := decode[GovernanceDefinition](version.Definition)
		if e != nil {
			return nil, nil, e
		}
		if definition.Standard == nil {
			return nil, nil, invalid
		}
		standards[binding.Standard] = *definition.Standard
		fields = append(fields, GovernanceStandardFieldAudit{Field: binding.Field, Standard: binding.Standard, Name: definition.Name, Ordinals: []int64{}})
	}
	return standards, fields, nil
}
func (s *Server) auditGovernanceStandards(ctx context.Context, p identity.Principal, def governanceProfileDefinition, stage *sql.Tx, guard func() error) (*GovernanceStandardAudit, error) {
	if len(def.StandardBindings) == 0 {
		return nil, nil
	}
	standards, fields, e := resolveStandardBindings(s.db.WithContext(ctx), p, def.StandardBindings)
	if e != nil {
		return nil, e
	}
	report := &GovernanceStandardAudit{Fields: fields}
	for i, binding := range def.StandardBindings {
		report.Fields[i].Status = "checked"
		for _, field := range def.Input.Fields {
			if field.Name == binding.Field && field.Sensitive {
				report.Fields[i].Status = "redacted"
			}
		}
	}
	rows, e := stage.QueryContext(ctx, "SELECT ordinal,payload FROM governance_input_stage ORDER BY ordinal")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		if report.Rows%500 == 0 {
			if e = guard(); e != nil {
				return nil, e
			}
		}
		var ordinal int64
		var raw json.RawMessage
		if e = rows.Scan(&ordinal, &raw); e != nil {
			return nil, e
		}
		var data DataRow
		if e = json.Unmarshal(raw, &data); e != nil {
			return nil, e
		}
		bad := false
		for i, binding := range def.StandardBindings {
			if report.Fields[i].Status == "redacted" {
				continue
			}
			standard := standards[binding.Standard]
			field := GovernanceField{Name: binding.Field, LogicalType: standard.LogicalType, Length: standard.Length, Precision: standard.Precision, Scale: standard.Scale, Nullable: standard.Nullable, Standard: &binding.Standard}
			value := DataRow{binding.Field: data[binding.Field]}
			if e = validateGovernanceOutputRow(value, GovernanceModel{Fields: []GovernanceField{field}}, standards); e != nil {
				bad = true
				report.Fields[i].Violations++
				if len(report.Fields[i].Ordinals) < 20 {
					report.Fields[i].Ordinals = append(report.Fields[i].Ordinals, ordinal)
				}
			}
		}
		report.Rows++
		if bad {
			report.RejectedRows++
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = guard(); e != nil {
		return nil, e
	}
	if _, _, e = resolveStandardBindings(s.db.WithContext(ctx), p, def.StandardBindings); e != nil {
		return nil, e
	}
	return report, nil
}
func (s *Server) registerGovernanceStandardCoverage(api, write *gin.RouterGroup) {
	write.POST("/governance/standard-audits", s.require("governance", "governor"), s.mutate("standard_audit", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			Parameters map[string]string           `json:"parameters"`
			Resource   Resource                    `json:"resource"`
			Bindings   []GovernanceStandardBinding `json:"bindings"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		if len(input.Bindings) < 1 || len(input.Bindings) > 100 {
			return 0, nil, "", invalid
		}
		names := []string{}
		for i := range input.Bindings {
			binding := &input.Bindings[i]
			if !valid(binding.Field, 128) || slices.Contains(names, binding.Field) || !valid(binding.Standard.ID, 64) || binding.Standard.Version < 1 || binding.Fingerprint != "" {
				return 0, nil, "", invalid
			}
			names = append(names, binding.Field)
			_, version, e := governanceRead(tx, p, "standard", binding.Standard.ID, binding.Standard.Version)
			if e != nil {
				return 0, nil, "", e
			}
			binding.Fingerprint = fingerprint(version.Definition)
		}
		if _, _, e = resolveStandardBindings(tx, p, input.Bindings); e != nil {
			return 0, nil, "", e
		}
		status, body, id, e := s.enqueueGovernanceProfile(tx, p, governanceProfileInput{Resource: input.Resource, Fields: names, Mode: "full", Parameters: input.Parameters}, nil)
		if e != nil {
			return 0, nil, id, e
		}
		var run GovernanceRun
		if e = json.Unmarshal(body, &run); e != nil {
			return 0, nil, id, e
		}
		def, e := decode[governanceProfileDefinition](run.Definition)
		if e != nil {
			return 0, nil, id, e
		}
		def.StandardBindings = input.Bindings
		run.Definition = encode(def)
		if e = tx.Model(&GovernanceRun{}).Where("id=?", id).Update("definition", run.Definition).Error; e != nil {
			return 0, nil, id, e
		}
		return status, encode(run), id, nil
	}))
	api.GET("/governance/standard-coverage", s.require("governance", "governor"), func(c *gin.Context) { s.governanceCoverage(c) })
}

type GovernanceCoverageField struct {
	Name     string         `json:"name"`
	Standard *GovernanceRef `json:"standard"`
	State    string         `json:"state"`
}
type GovernanceCoverageRow struct {
	ID       string                    `json:"id"`
	Version  int                       `json:"version"`
	Name     string                    `json:"name"`
	Kind     string                    `json:"kind"`
	Fields   []GovernanceCoverageField `json:"fields"`
	Covered  int                       `json:"covered"`
	Total    int                       `json:"total"`
	Resource *Resource                 `json:"resource"`
}

func (s *Server) governanceCoverage(c *gin.Context) {
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 15, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	q := tx.Table("governance_definitions d").Select("d.id,d.kind,v.version,v.definition").Joins("JOIN governance_versions v ON v.definition_id=d.id AND v.state='published' AND NOT EXISTS(SELECT 1 FROM governance_versions newer WHERE newer.definition_id=d.id AND newer.state='published' AND newer.version>v.version)").Where("d.kind IN ('model','metadata')")
	if !admin(p) {
		q = q.Where("d.department_id=?", p.DepartmentID)
	}
	if scope := workspaceScope(c.Request.Context()); scope.ID != "" {
		q = q.Where("d.workspace_id=? OR (d.kind='metadata' AND EXISTS(SELECT 1 FROM governance_workspace_sources w WHERE w.workspace_id=? AND w.active AND w.resource_id=v.definition->'metadata'->>'object_id'))", scope.ID, scope.ID)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var records []struct {
		ID, Kind   string
		Version    int
		Definition json.RawMessage
	}
	if total > 1000 {
		s.fail(c, problem(409, "GOVERNANCE_COVERAGE_LIMIT", "当前定义超过千项，请在具体空间查看落标覆盖"))
		return
	}
	if e = q.Order("d.created_at DESC,d.id").Limit(1000).Scan(&records).Error; e != nil {
		s.fail(c, e)
		return
	}
	result := []GovernanceCoverageRow{}
	for _, record := range records {
		definition, e := decode[GovernanceDefinition](record.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		row := GovernanceCoverageRow{ID: record.ID, Version: record.Version, Kind: record.Kind, Name: definition.Name, Fields: []GovernanceCoverageField{}}
		if definition.Model != nil {
			for _, field := range definition.Model.Fields {
				row.Fields = append(row.Fields, GovernanceCoverageField{Name: field.Name, Standard: field.Standard})
			}
		}
		if definition.Metadata != nil {
			_, target, e := metadataTarget(tx, p, definition.Metadata.ObjectID, false)
			if e != nil {
				if e == denied {
					continue
				}
				s.fail(c, e)
				return
			}
			ref := Resource{Type: target.ResourceType, ID: target.ObjectID, Version: target.SchemaVersion}
			row.Resource = &ref
			for _, field := range target.Fields {
				var standard *GovernanceRef
				for _, annotation := range definition.Metadata.Fields {
					if annotation.Name == field.Name {
						standard = annotation.Standard
					}
				}
				row.Fields = append(row.Fields, GovernanceCoverageField{Name: field.Name, Standard: standard})
			}
		}
		row.Total = len(row.Fields)
		for i := range row.Fields {
			field := &row.Fields[i]
			field.State = "unmapped"
			if field.Standard != nil {
				_, version, e := governanceRead(tx, p, "standard", field.Standard.ID, field.Standard.Version)
				if e == nil && version.State == "published" {
					field.State = "mapped"
					row.Covered++
				} else {
					field.State = "unavailable"
				}
			}
		}
		result = append(result, row)
	}
	total = int64(len(result))
	start := min((page-1)*size, len(result))
	end := min(start+size, len(result))
	result = result[start:end]
	c.JSON(200, gin.H{"items": result, "total": total, "page": page, "page_size": size, "scope": "latest_published_definitions"})
}
