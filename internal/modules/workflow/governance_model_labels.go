package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strings"
)

// Labels are display context, separate from immutable model definitions and execution fingerprints.
func (s *Server) registerGovernanceModelLabels(api *gin.RouterGroup) {
	api.GET("/governance/models/:id/versions/:version/field-labels", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		head, row, e := governanceRead(tx, p, "model", c.Param("id"), version)
		if e != nil {
			s.fail(c, e)
			return
		}
		definition, e := decode[GovernanceDefinition](row.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		if definition.Model == nil {
			s.fail(c, invalid)
			return
		}
		labels, e := s.governanceModelLabels(tx, p, head, version, *definition.Model)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"field_labels": labels})
	})
}
func (s *Server) governanceModelLabels(tx *gorm.DB, p identity.Principal, head GovernanceDefinitionRecord, version int, model GovernanceModel) (map[string]string, error) {
	candidates := map[string]map[string]bool{}
	wanted := map[string]bool{}
	for _, field := range model.Fields {
		if strings.TrimSpace(field.BusinessName) == "" && strings.TrimSpace(field.Description) == "" {
			wanted[field.Name] = true
		}
	}
	add := func(name, label string) {
		label = strings.TrimSpace(label)
		if !wanted[name] || label == "" {
			return
		}
		if candidates[name] == nil {
			candidates[name] = map[string]bool{}
		}
		candidates[name][label] = true
	}
	// Only committed outputs of this exact model version, in its workspace, supply fallback labels.
	q := tx.Table("governance_runs r").Select("DISTINCT r.result->'field_labels' AS labels").Joins("JOIN governance_outputs o ON o.run_id=r.id").Where("r.kind='dataflow' AND r.state='succeeded' AND jsonb_typeof(r.result->'field_labels')='object' AND r.result->'model'->>'id'=? AND (r.result->'model'->>'version')::integer=? AND r.workspace_id=?", head.ID, version, workspaceID(head.WorkspaceID))
	if !admin(p) {
		q = q.Where("r.actor_id=? AND r.department_id=?", p.UserID, p.DepartmentID)
	}
	rows := []struct{ Labels json.RawMessage }{}
	if e := q.Limit(101).Find(&rows).Error; e != nil {
		return nil, e
	}
	if len(rows) <= 100 {
		for _, row := range rows {
			labels, e := decode[map[string]string](row.Labels)
			if e != nil {
				return nil, e
			}
			for name, label := range labels {
				add(name, label)
			}
		}
	}
	// Entity models may not have a governed output yet; follow explicit source mappings, never global English-name guesses.
	masters := []struct{ Definition json.RawMessage }{}
	mq := tx.Table("governance_definitions d").Select("v.definition").Joins("JOIN governance_versions v ON v.definition_id=d.id AND v.version=d.latest_version").Where("d.kind='master' AND d.workspace_id=? AND v.definition->'master'->'model_ref'->>'id'=? AND (v.definition->'master'->'model_ref'->>'version')::integer=?", workspaceID(head.WorkspaceID), head.ID, version)
	if !admin(p) {
		mq = mq.Where("d.department_id=?", p.DepartmentID)
	}
	if e := mq.Order("d.id").Limit(101).Find(&masters).Error; e != nil {
		return nil, e
	}
	if len(masters) <= 100 {
		for _, row := range masters {
			definition, e := decode[GovernanceDefinition](row.Definition)
			if e != nil {
				return nil, e
			}
			if definition.Master == nil {
				continue
			}
			for _, source := range definition.Master.Sources {
				record, target, e := metadataTarget(tx, p, source.Resource.ID, false)
				if e != nil {
					var diagnostic *Problem
					if errors.Is(e, gorm.ErrRecordNotFound) || (errors.As(e, &diagnostic) && (diagnostic.Status == 403 || diagnostic.Status == 404 || diagnostic.Status == 410)) {
						continue
					}
					return nil, e
				}
				if target.ResourceType != source.Resource.Type || record.Version != source.Resource.Version {
					continue
				}
				for name, original := range source.Mapping {
					add(name, target.FieldLabels[original])
				}
			}
		}
	}
	labels := map[string]string{}
	for name, values := range candidates {
		if len(values) == 1 {
			for label := range values {
				labels[name] = label
			}
		}
	}
	return labels, nil
}
