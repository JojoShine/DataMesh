package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceRetentionPolicy struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	DepartmentID string    `json:"department_id"`
	ActorID      string    `json:"actor_id"`
	FlowID       string    `json:"flow_id"`
	Days         int       `json:"days"`
	KeepLast     int       `json:"keep_last"`
	Enabled      bool      `json:"enabled"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (GovernanceRetentionPolicy) TableName() string { return "governance_retention_policies" }

type GovernanceSnapshotPurge struct {
	StorageCleanedAt *time.Time `json:"storage_cleaned_at,omitempty"`
	RunID            string     `json:"run_id" gorm:"primaryKey"`
	ActorID          string     `json:"actor_id"`
	DeletedRows      int64      `json:"deleted_rows"`
	EvidenceHash     string     `json:"evidence_hash"`
	PolicyID         *string    `json:"policy_id"`
	CreatedAt        time.Time  `json:"created_at"`
}

func (GovernanceSnapshotPurge) TableName() string { return "governance_snapshot_purges" }

type GovernanceMigrationPlan struct {
	ID           string          `json:"id" gorm:"primaryKey"`
	DepartmentID string          `json:"department_id"`
	ActorID      string          `json:"actor_id"`
	SourceID     string          `json:"source_id"`
	TargetID     string          `json:"target_id"`
	State        string          `json:"state"`
	Drafts       json.RawMessage `json:"drafts" gorm:"type:jsonb"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (GovernanceMigrationPlan) TableName() string { return "governance_migration_plans" }
func governanceLifecycleTarget(tx *gorm.DB, p identity.Principal, id string, lock bool) (Record, GovernanceRun, error) {
	r, e := get(tx, "dataset", id, lock)
	if e != nil {
		return r, GovernanceRun{}, e
	}
	if !department(p, r) || (!admin(p) && r.OwnerID != p.UserID) {
		return r, GovernanceRun{}, denied
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "dataset", ID: id}, "manage_resource"); e != nil {
		return r, GovernanceRun{}, e
	}
	var run GovernanceRun
	e = tx.Where("id=? AND kind='dataflow' AND state='succeeded' AND EXISTS(SELECT 1 FROM governance_outputs o WHERE o.run_id=governance_runs.id AND o.dataset_id=?)", id, id).Take(&run).Error
	if e == nil {
		e = governanceWorkspaceMatch(tx.Statement.Context, run.WorkspaceID)
		if e == nil && workspaceScope(tx.Statement.Context).Mutation {
			_, e = governanceWorkspaceWrite(tx, p, run.WorkspaceID)
		}
	}
	return r, run, e
}

type governancePurgePreview struct {
	DatasetID    string                   `json:"dataset_id"`
	Rows         int64                    `json:"rows"`
	Blockers     []string                 `json:"blockers"`
	Dependencies []governanceDependency   `json:"dependencies"`
	Restricted   int                      `json:"restricted_dependencies"`
	Token        string                   `json:"token"`
	Purged       *GovernanceSnapshotPurge `json:"purged,omitempty"`
}

func governancePurgeCheck(tx *gorm.DB, p identity.Principal, r Record, run GovernanceRun, excludePlan string) (governancePurgePreview, error) {
	out := governancePurgePreview{DatasetID: r.ID, Blockers: []string{}, Dependencies: []governanceDependency{}}
	var purges []GovernanceSnapshotPurge
	if e := tx.Where("run_id=?", run.ID).Limit(1).Find(&purges).Error; e != nil {
		return out, e
	}
	if len(purges) > 0 {
		out.Purged = &purges[0]
		out.Blockers = append(out.Blockers, "SNAPSHOT_ALREADY_PURGED")
	}
	if r.State == "published" {
		out.Blockers = append(out.Blockers, "OUTPUT_PUBLISHED")
	}
	if run.FinishedAt == nil || time.Since(*run.FinishedAt) < 24*time.Hour {
		out.Blockers = append(out.Blockers, "MINIMUM_RETENTION_24H")
	}
	deps, e := governanceDependencies(tx, r.ID, true)
	if e != nil {
		return out, e
	}
	for _, d := range deps {
		if governanceDependencyVisible(tx, p, d) {
			out.Dependencies = append(out.Dependencies, d)
		} else {
			out.Restricted++
		}
	}
	if len(deps) > 0 {
		out.Blockers = append(out.Blockers, "DOWNSTREAM_IN_USE")
	}
	var active int64
	if e = tx.Model(&GovernanceQualityIssue{}).Where("state NOT IN ('closed','superseded') AND (run_id=? OR retest_run_id=?)", run.ID, run.ID).Count(&active).Error; e != nil {
		return out, e
	}
	if active > 0 {
		out.Blockers = append(out.Blockers, "QUALITY_ISSUE_OPEN")
	}
	if e = tx.Table("governance_workflow_tasks t").Joins("JOIN governance_runs r ON r.id=t.parent_run_id").Where("t.child_run_id=? AND r.state IN ('queued','running')", run.ID).Count(&active).Error; e != nil {
		return out, e
	}
	if active > 0 {
		out.Blockers = append(out.Blockers, "WORKFLOW_IN_USE")
	}
	if e = tx.Model(&GovernanceMigrationPlan{}).Where("state='open' AND id<>? AND (source_id=? OR target_id=?)", excludePlan, r.ID, r.ID).Count(&active).Error; e != nil {
		return out, e
	}
	if active > 0 {
		out.Blockers = append(out.Blockers, "MIGRATION_IN_PROGRESS")
	}
	var policies []GovernanceRetentionPolicy
	if e = tx.Where("actor_id=? AND flow_id=?", r.OwnerID, r.ParentID).Find(&policies).Error; e != nil {
		return out, e
	}
	for _, policy := range policies {
		var newer int64
		if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND state='succeeded' AND definition->'flow'->>'id'=? AND (created_at,id)>(?,?) AND NOT EXISTS(SELECT 1 FROM governance_snapshot_purges p WHERE p.run_id=governance_runs.id)", r.OwnerID, r.ParentID, run.CreatedAt, run.ID).Count(&newer).Error; e != nil {
			return out, e
		}
		if newer < int64(policy.KeepLast) {
			out.Blockers = append(out.Blockers, "KEEP_RECENT_RESULTS")
		}
		if run.FinishedAt == nil || time.Since(*run.FinishedAt) < time.Duration(policy.Days)*24*time.Hour {
			out.Blockers = append(out.Blockers, "RETENTION_NOT_EXPIRED")
		}
	}
	if e = tx.Table("governance_flow_rows").Where("run_id=?", run.ID).Count(&out.Rows).Error; e != nil {
		return out, e
	}
	report, e := decode[governanceFlowReport](run.Result)
	if e != nil {
		return out, e
	}
	if report.Storage != nil {
		out.Rows = report.OutputRows + report.RejectedRows
	}
	out.Token = fingerprint(encode(gin.H{"id": r.ID, "state": r.State, "version": r.Version, "rows": out.Rows, "result": run.Result, "definition": run.Definition, "blockers": out.Blockers, "dependencies": deps}))
	return out, nil
}
func (s *Server) purgeGovernanceSnapshot(tx *gorm.DB, p identity.Principal, id, token, policyID string) (int, json.RawMessage, string, error) {
	// Same lock order as definition changes and flow queueing; all checks repeat under the locks.
	r, _, e := governanceLifecycleTarget(tx, p, id, false)
	if e != nil {
		return 0, nil, id, e
	}
	if e = governanceLock(tx, r.DepartmentID); e != nil {
		return 0, nil, id, e
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return 0, nil, id, e
	}
	r, run, e := governanceLifecycleTarget(tx, p, id, true)
	if e != nil {
		return 0, nil, id, e
	}
	preview, e := governancePurgeCheck(tx, p, r, run, "")
	if e != nil {
		return 0, nil, id, e
	}
	if len(preview.Blockers) > 0 {
		return 0, nil, id, problem(409, "GOVERNANCE_PURGE_PROTECTED", "成果仍在保留期或有下游依赖，请先处理预检阻断项")
	}
	if token != "" && token != preview.Token {
		return 0, nil, id, problem(409, "GOVERNANCE_PURGE_CHANGED", "成果状态已变化，请重新预检")
	}
	result := tx.Exec("DELETE FROM governance_flow_rows WHERE run_id=?", run.ID)
	if result.Error != nil {
		return 0, nil, id, result.Error
	}
	receipt := GovernanceSnapshotPurge{RunID: run.ID, ActorID: p.UserID, DeletedRows: preview.Rows, EvidenceHash: fingerprint(encode(gin.H{"definition": run.Definition, "result": run.Result, "dataset": r.Body}))}
	if policyID != "" {
		receipt.PolicyID = &policyID
	}
	if e = tx.Create(&receipt).Error; e != nil {
		return 0, nil, id, e
	}
	r.State = "retired"
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	if e = governanceRunEvent(tx, run, "SNAPSHOT_PURGED"); e != nil {
		return 0, nil, id, e
	}
	return 200, encode(receipt), id, nil
}
func (s *Server) registerGovernanceLifecycle(api, write *gin.RouterGroup) {
	api.GET("/governance/lifecycle", s.require("governance", "governor"), func(c *gin.Context) {
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
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
		q := workspaceQuery(tx.Model(&Record{}).Where("kind='dataset' AND body->>'governance_run_id'<>''"), workspaceDatasetColumn)
		if !admin(p) {
			q = q.Where("owner_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		var total int64
		rows := []Record{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		items := []gin.H{}
		for _, r := range rows {
			if e = requireResourceCapability(tx, p, Resource{Type: "dataset", ID: r.ID}, "manage_resource"); e != nil {
				continue
			}
			d, e := decode[DatasetData](r.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			items = append(items, gin.H{"id": r.ID, "name": d.Definition.Name, "state": r.State, "flow_id": r.ParentID, "created_at": r.CreatedAt})
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	})
	api.GET("/governance/lifecycle/:id/preview", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		r, run, e := governanceLifecycleTarget(tx, p, c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		out, e := governancePurgeCheck(tx, p, r, run, "")
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, out)
	})
	write.POST("/governance/lifecycle/:id/purge", s.require("governance", "governor"), s.mutatePath("governance_snapshot_purge", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			Confirmation string `json:"confirmation"`
			Token        string `json:"token"`
		}](b)
		if e != nil {
			return 0, nil, id, e
		}
		if v.Confirmation != id || v.Token == "" {
			return 0, nil, id, invalid
		}
		return s.purgeGovernanceSnapshot(tx, p, id, v.Token, "")
	}))
	api.GET("/governance/retention-policies", s.require("governance", "governor"), func(c *gin.Context) {
		p := principal(c)
		rows := []GovernanceRetentionPolicy{}
		q := workspaceQuery(s.db.WithContext(c.Request.Context()), "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_retention_policies.flow_id)")
		if !admin(p) {
			q = q.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		if e := q.Order("created_at DESC,id").Limit(100).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "limit": 100})
	})
	write.POST("/governance/retention-policies", s.require("governance", "governor"), s.mutate("governance_retention_policy", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			FlowID          string `json:"flow_id"`
			Days            int    `json:"days"`
			KeepLast        int    `json:"keep_last"`
			Enabled         bool   `json:"enabled"`
			ExpectedVersion int    `json:"expected_version"`
			Confirmation    string `json:"confirmation"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		if v.Days < 1 || v.Days > 3650 || v.KeepLast < 1 || v.KeepLast > 1000 || v.Enabled && v.Confirmation != v.FlowID {
			return 0, nil, "", invalid
		}
		h, _, e := governanceRead(tx, p, "dataflow", v.FlowID, 0)
		if e != nil {
			return 0, nil, "", e
		}
		if e = governanceLock(tx, h.DepartmentID); e != nil {
			return 0, nil, "", e
		}
		var rows []GovernanceRetentionPolicy
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("actor_id=? AND flow_id=?", p.UserID, v.FlowID).Find(&rows).Error; e != nil {
			return 0, nil, "", e
		}
		r := GovernanceRetentionPolicy{ActorID: p.UserID, DepartmentID: h.DepartmentID, FlowID: v.FlowID, Version: 1}
		status := 201
		if len(rows) > 0 {
			r = rows[0]
			if r.Version != v.ExpectedVersion {
				return 0, nil, r.ID, conflict
			}
			r.Version++
			status = 200
		} else {
			if v.ExpectedVersion != 0 {
				return 0, nil, "", conflict
			}
			r.ID, e = newID()
			if e != nil {
				return 0, nil, "", e
			}
		}
		r.Days = v.Days
		r.KeepLast = v.KeepLast
		r.Enabled = v.Enabled
		r.UpdatedAt = time.Now().UTC()
		if e = tx.Save(&r).Error; e != nil {
			return 0, nil, r.ID, e
		}
		return status, encode(r), r.ID, nil
	}))
	s.registerGovernanceMigrations(api, write)
}
func (s *Server) sweepGovernanceRetention(ctx context.Context) error {
	if e := s.cleanupGovernanceStorage(ctx); e != nil {
		return e
	}
	policies := []GovernanceRetentionPolicy{}
	if e := s.db.WithContext(ctx).Where("enabled=true").Order("updated_at,id").Limit(1000).Find(&policies).Error; e != nil {
		return e
	}
	for _, policy := range policies {
		p, e := s.etlPrincipal(ctx, policy.ActorID)
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			return e
		}
		if !admin(p) && (!slices.Contains(p.Roles, "governor") || !slices.Contains(p.Platforms, "governance") || p.DepartmentID != policy.DepartmentID) {
			continue
		}
		rows := []Record{}
		if e = s.db.WithContext(ctx).Where("kind='dataset' AND owner_id=? AND parent_id=? AND state IN ('draft','withdrawn') AND created_at<? AND NOT EXISTS(SELECT 1 FROM governance_snapshot_purges p WHERE p.run_id=business_records.id)", p.UserID, policy.FlowID, time.Now().Add(-time.Duration(policy.Days)*24*time.Hour)).Order("created_at,id").Limit(20).Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if er := governanceLock(tx, policy.DepartmentID); er != nil {
					return er
				}
				var current GovernanceRetentionPolicy
				if er := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", policy.ID).Take(&current).Error; er != nil {
					return er
				}
				if !current.Enabled || current.Version != policy.Version {
					return nil
				}
				_, run, er := governanceLifecycleTarget(tx, p, r.ID, false)
				if er != nil {
					return er
				}
				preview, er := governancePurgeCheck(tx, p, r, run, "")
				if er != nil {
					return er
				}
				if len(preview.Blockers) > 0 {
					return nil
				}
				_, _, _, er = s.purgeGovernanceSnapshot(tx, p, r.ID, "", policy.ID)
				return er
			})
			if e != nil {
				var pr *Problem
				if asProblem(e, &pr) && pr.Status < 500 {
					continue
				}
				return e
			}
		}
	}
	return nil
}
func (s *Server) registerGovernanceMigrations(api, write *gin.RouterGroup) {
	api.GET("/governance/migrations", s.require("governance", "governor"), func(c *gin.Context) {
		p := principal(c)
		q := workspaceQuery(s.db.WithContext(c.Request.Context()), "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_migration_plans.source_id)")
		if !admin(p) {
			q = q.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		rows := []GovernanceMigrationPlan{}
		if e := q.Order("created_at DESC,id").Limit(100).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "limit": 100})
	})
	write.POST("/governance/migrations", s.require("governance", "governor"), s.mutate("governance_migration_create", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		if v.SourceID == v.TargetID {
			return 0, nil, "", invalid
		}
		source, _, e := governanceLifecycleTarget(tx, p, v.SourceID, false)
		if e != nil {
			return 0, nil, "", e
		}
		if e = governanceLock(tx, source.DepartmentID); e != nil {
			return 0, nil, "", e
		}
		target, _, e := governanceLifecycleTarget(tx, p, v.TargetID, true)
		if e != nil {
			return 0, nil, "", e
		}
		if target.DepartmentID != source.DepartmentID || !slices.Contains([]string{"draft", "published"}, target.State) {
			return 0, nil, "", problem(409, "GOVERNANCE_MIGRATION_TARGET", "请选择同部门且可用的新成果")
		}
		a, e := decode[DatasetData](source.Body)
		if e != nil {
			return 0, nil, "", e
		}
		bdata, _, _, _, e := s.governanceOutputReady(tx.Statement.Context, tx, target)
		if e != nil {
			return 0, nil, "", e
		}
		if !governanceMigrationCompatible(a.Definition.Fields, bdata.Definition.Fields) {
			return 0, nil, "", problem(409, "GOVERNANCE_MIGRATION_SCHEMA", "目标成果字段类型、空值或敏感性与原成果不兼容")
		}
		id, e := newID()
		if e != nil {
			return 0, nil, "", e
		}
		r := GovernanceMigrationPlan{ID: id, ActorID: p.UserID, DepartmentID: source.DepartmentID, SourceID: v.SourceID, TargetID: v.TargetID, State: "open", Drafts: json.RawMessage("[]")}
		if e = tx.Create(&r).Error; e != nil {
			return 0, nil, id, e
		}
		return 201, encode(r), id, nil
	}))
	for _, action := range []string{"draft", "complete", "cancel"} {
		write.POST("/governance/migrations/:id/"+action, s.require("governance", "governor"), s.mutatePath("governance_migration_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			v, e := bind[struct {
				DefinitionID string `json:"definition_id"`
			}](b)
			if e != nil {
				return 0, nil, id, e
			}
			var r GovernanceMigrationPlan
			if e = tx.Where("id=?", id).Take(&r).Error; e != nil {
				return 0, nil, id, e
			}
			if !admin(p) && (r.ActorID != p.UserID || r.DepartmentID != p.DepartmentID) {
				return 0, nil, id, denied
			}
			if e = governanceLock(tx, r.DepartmentID); e != nil {
				return 0, nil, id, e
			}
			if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&r).Error; e != nil {
				return 0, nil, id, e
			}
			if r.State != "open" {
				return 0, nil, id, conflict
			}
			switch action {
			case "cancel":
				r.State = "cancelled"
			case "complete":
				target, _, er := governanceLifecycleTarget(tx, p, r.TargetID, true)
				if er != nil {
					return 0, nil, id, er
				}
				if !slices.Contains([]string{"draft", "published"}, target.State) {
					return 0, nil, id, conflict
				}
				if _, _, _, _, er = s.governanceOutputReady(tx.Statement.Context, tx, target); er != nil {
					return 0, nil, id, er
				}
				deps, e := governanceDependencies(tx, r.SourceID, true)
				if e != nil {
					return 0, nil, id, e
				}
				if len(deps) > 0 {
					return 0, nil, id, problem(409, "GOVERNANCE_MIGRATION_REFERENCED", "原成果仍被定义、挂接、授权或运行引用，请完成迁移后重试")
				}
				r.State = "completed"
			case "draft":
				_, row, e := governanceRead(tx, p, "dataflow", v.DefinitionID, 0)
				if e != nil {
					return 0, nil, id, e
				}
				d, e := decode[GovernanceDefinition](row.Definition)
				if e != nil {
					return 0, nil, id, e
				}
				target, _, e := governanceLifecycleTarget(tx, p, r.TargetID, true)
				if e != nil {
					return 0, nil, id, e
				}
				if !slices.Contains([]string{"draft", "published"}, target.State) {
					return 0, nil, id, conflict
				}
				_, _, _, _, e = s.governanceOutputReady(tx.Statement.Context, tx, target)
				if e != nil {
					return 0, nil, id, e
				}
				changed := false
				if d.Dataflow != nil {
					for i := range d.Dataflow.Nodes {
						src := d.Dataflow.Nodes[i].Source
						if src != nil && src.Resource.ID == r.SourceID {
							src.Resource = Resource{Type: "governed_dataset", ID: target.ID, Version: target.Version}
							changed = true
						}
					}
				}
				if !changed {
					return 0, nil, id, problem(409, "GOVERNANCE_MIGRATION_REFERENCE", "定义最新版本未引用原成果")
				}
				_, draft, _, e := s.governanceSave(tx, p, "dataflow", v.DefinitionID, encode(governanceWrite{BaseVersion: row.Version, Definition: d, ChangeNote: fmt.Sprintf("成果迁移 %s → %s；待原流程审核发布", r.SourceID, r.TargetID)}))
				if e != nil {
					return 0, nil, id, e
				}
				var refs []json.RawMessage
				if e = json.Unmarshal(r.Drafts, &refs); e != nil {
					return 0, nil, id, e
				}
				refs = append(refs, draft)
				r.Drafts = encode(refs)
			}
			r.UpdatedAt = time.Now().UTC()
			if e = tx.Save(&r).Error; e != nil {
				return 0, nil, id, e
			}
			return 200, encode(r), id, nil
		}))
	}
}
func governanceMigrationCompatible(a, b []Field) bool {
	for _, x := range a {
		at := slices.IndexFunc(b, func(f Field) bool { return f.Name == x.Name })
		if at < 0 {
			return false
		}
		y := b[at]
		if x.LogicalType != y.LogicalType || (!x.Nullable && y.Nullable) || x.Sensitive != y.Sensitive || (x.PrimaryKey && !y.PrimaryKey) || !reflect.DeepEqual(x.Precision, y.Precision) || !reflect.DeepEqual(x.Scale, y.Scale) {
			return false
		}
	}
	return true
}
