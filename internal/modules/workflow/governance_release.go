package workflow

import (
	"context"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"time"
)

type GovernanceRelease struct {
	WorkspaceID     string             `json:"workspace_id"`
	Kind            string             `json:"kind"`
	Candidate       GovernanceRef      `json:"candidate"`
	CandidateDigest string             `json:"candidate_digest"`
	BaseVersion     int                `json:"base_version"`
	Reason          string             `json:"reason"`
	MigrationNote   string             `json:"migration_note"`
	Changes         []GovernanceChange `json:"changes"`
	Impact          []GovernanceRef    `json:"impact"`
	TrialRunID      string             `json:"trial_run_id"`
	ReviewerID      string             `json:"reviewer_id"`
	ReviewNote      string             `json:"review_note"`
	ReviewedAt      *time.Time         `json:"reviewed_at"`
}
type governanceTrial struct {
	ReleaseID string `json:"release_id"`
	RunID     string `json:"run_id"`
	Digest    string `json:"digest"`
}
type governanceTrialContextKey struct{}

func releaseRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (Record, GovernanceRelease, error) {
	row, e := get(tx, "governance_release", id, lock)
	if e != nil {
		return row, GovernanceRelease{}, e
	}
	if !admin(p) && row.DepartmentID != p.DepartmentID {
		return row, GovernanceRelease{}, denied
	}
	body, e := decode[GovernanceRelease](row.Body)
	if e != nil {
		return row, body, e
	}
	if e = governanceWorkspaceMatch(tx.Statement.Context, body.WorkspaceID); e != nil {
		return row, body, e
	}
	_, _, e = governanceRead(tx, p, body.Kind, body.Candidate.ID, body.Candidate.Version)
	return row, body, e
}
func (s *Server) releaseTrialEvidence(tx *gorm.DB, p identity.Principal, body GovernanceRelease) error {
	if body.Kind == "workflow" {
		return s.workflowReleaseEvidence(tx, p, body)
	}
	if body.Kind != "dataflow" {
		return nil
	}
	var run GovernanceRun
	if body.TrialRunID == "" {
		return problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "请先完成此候选版本的完整试运行")
	}
	if e := tx.Where("id=? AND workspace_id=?", body.TrialRunID, body.WorkspaceID).Take(&run).Error; e != nil {
		return e
	}
	if run.State != "succeeded" || run.Kind != "dataflow" {
		return problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "候选版本尚未完成成功的完整试运行")
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		return e
	}
	if def.Trial == nil || def.Trial.Digest != body.CandidateDigest || def.Flow != body.Candidate || def.Trial.RunID != run.ID {
		return conflict
	}
	actor, e := s.etlPrincipal(tx.Statement.Context, run.ActorID)
	if e != nil {
		return denied
	}
	if e = s.governanceFlowAuthorize(governanceReadContext(tx.Statement.Context), actor, def); e != nil {
		return e
	}
	report, e := decode[governanceFlowReport](run.Result)
	if e != nil {
		return e
	}
	if report.RejectedRows > 0 {
		return problem(409, "GOVERNANCE_RELEASE_QUALITY", "试运行仍有隔离记录，请整改后重试")
	}
	return nil
}
func (s *Server) governanceReleaseGate(tx *gorm.DB, p identity.Principal, kind, id string, version int, definition json.RawMessage) error {
	var proposals []Record
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind='governance_release' AND body->>'kind'=? AND body->'candidate'->>'id'=? AND (body->'candidate'->>'version')::integer=? AND state IN ('submitted','approved','rejected')", kind, id, version).Order("created_at DESC,id DESC").Limit(1).Find(&proposals).Error; e != nil {
		return e
	}
	if len(proposals) == 0 {
		var head GovernanceDefinitionRecord
		if e := tx.Where("id=? AND kind=?", id, kind).Take(&head).Error; e != nil {
			return e
		}
		if head.WorkspaceID != nil {
			var space GovernanceWorkspace
			if e := tx.Where("id=?", *head.WorkspaceID).Take(&space).Error; e != nil {
				return e
			}
			if slices.Contains(space.RequiredReleaseKinds, kind) {
				return problem(409, "GOVERNANCE_RELEASE_REVIEW_REQUIRED", "当前空间要求此类型先提交变更申请并完成独立审核")
			}
		}
	}
	for _, row := range proposals {
		body, e := decode[GovernanceRelease](row.Body)
		if e != nil {
			return e
		}
		if row.State != "approved" || body.ReviewerID == "" || body.ReviewerID == row.OwnerID || body.CandidateDigest != fingerprint(definition) {
			return problem(409, "GOVERNANCE_RELEASE_REVIEW_REQUIRED", "此版本已进入变更流程，请完成独立审核后发布")
		}
		if e = s.releaseTrialEvidence(tx, p, body); e != nil {
			return e
		}
		row.State = "published"
		row.Version++
		if e = save(tx, &row); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) registerGovernanceReleases(api, write *gin.RouterGroup) {
	api.GET("/governance/releases/:id", s.require("governance", "governor"), func(c *gin.Context) {
		row, _, e := releaseRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.Data(200, "application/json", governanceCapabilityRecord(row))
	})
	api.GET("/governance/releases", s.require("governance", "governor"), func(c *gin.Context) {
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
		q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind='governance_release'")
		if scope := workspaceScope(c.Request.Context()); scope.ID != "" {
			q = q.Where("body->>'workspace_id' IN (?,'')", scope.ID)
		}
		if !admin(principal(c)) {
			q = q.Where("department_id=?", principal(c).DepartmentID)
		}
		var total int64
		rows := []Record{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, row := range rows {
			items = append(items, governanceCapabilityRecord(row))
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/releases", s.require("governance", "governor"), s.mutate("release_submit", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			Kind          string        `json:"kind"`
			Candidate     GovernanceRef `json:"candidate"`
			BaseVersion   int           `json:"base_version"`
			Reason        string        `json:"reason"`
			MigrationNote string        `json:"migration_note"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		if !slices.Contains([]string{"master", "scenario", "model", "standard", "term", "metadata", "dataflow", "workflow", "quality_template"}, input.Kind) || !valid(input.Reason, 4000) || len(input.MigrationNote) > 4000 || input.BaseVersion < 0 || input.BaseVersion >= input.Candidate.Version {
			return 0, nil, "", invalid
		}
		head, candidate, e := governanceRead(tx, p, input.Kind, input.Candidate.ID, input.Candidate.Version)
		if e != nil {
			return 0, nil, "", e
		}
		if e = governanceLock(tx, head.DepartmentID); e != nil {
			return 0, nil, "", e
		}
		if candidate.State != "draft" || head.LatestVersion != candidate.Version {
			return 0, nil, "", conflict
		}
		definition, e := decode[GovernanceDefinition](candidate.Definition)
		if e != nil {
			return 0, nil, "", e
		}
		if e = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), definition); e != nil {
			return 0, nil, "", e
		}
		var active int64
		if e = tx.Model(&Record{}).Where("kind='governance_release' AND body->'candidate'->>'id'=? AND (body->'candidate'->>'version')::integer=? AND state IN ('submitted','approved')", head.ID, candidate.Version).Count(&active).Error; e != nil {
			return 0, nil, "", e
		}
		if active > 0 {
			return 0, nil, "", conflict
		}
		var previous GovernanceVersion
		if e = tx.Where("definition_id=? AND version<? AND published_at IS NOT NULL", head.ID, candidate.Version).Order("version DESC").Limit(1).Find(&previous).Error; e != nil {
			return 0, nil, "", e
		}
		if input.BaseVersion == 0 {
			input.BaseVersion = previous.Version
		}
		if input.BaseVersion != previous.Version {
			return 0, nil, "", problem(409, "GOVERNANCE_RELEASE_BASE", "请以最近一次已发布版本为比较基准")
		}
		before := GovernanceDefinition{}
		if input.BaseVersion > 0 {
			_, base, e := governanceRead(tx, p, input.Kind, head.ID, input.BaseVersion)
			if e != nil {
				return 0, nil, "", e
			}
			if base.PublishedAt == nil {
				return 0, nil, "", invalid
			}
			before, e = decode[GovernanceDefinition](base.Definition)
			if e != nil {
				return 0, nil, "", e
			}
		}
		changes := governanceChanges(input.Kind, before, definition)
		for _, change := range changes {
			if input.BaseVersion > 0 && change.Breaking && !valid(input.MigrationNote, 4000) {
				return 0, nil, "", problem(409, "MODEL_MIGRATION_REQUIRED", "破坏性变更需要填写下游迁移及恢复安排")
			}
		}
		refs := []GovernanceRef{}
		dependencies, e := governanceDependencies(tx, head.ID, true)
		if e != nil {
			return 0, nil, "", e
		}
		for _, dependency := range dependencies {
			if dependency.Kind != "definition" || !governanceDependencyVisible(tx, p, dependency) {
				continue
			}
			var target GovernanceDefinitionRecord
			if e = tx.Where("id=?", dependency.ID).Take(&target).Error; e != nil {
				return 0, nil, "", e
			}
			if governanceWorkspaceMatch(tx.Statement.Context, workspaceID(target.WorkspaceID)) == nil {
				refs = append(refs, GovernanceRef{ID: dependency.ID, Version: dependency.Version})
			}
		}
		body := GovernanceRelease{WorkspaceID: workspaceID(head.WorkspaceID), Kind: input.Kind, Candidate: input.Candidate, CandidateDigest: fingerprint(candidate.Definition), BaseVersion: input.BaseVersion, Reason: input.Reason, MigrationNote: input.MigrationNote, Changes: changes, Impact: refs}
		row, e := newRecord("governance_release", head.DepartmentID, p.UserID, head.ID, "submitted", body)
		if e != nil {
			return 0, nil, "", e
		}
		e = tx.Create(&row).Error
		return 201, governanceCapabilityRecord(row), row.ID, e
	}))
	write.POST("/governance/releases/:id/trial", s.require("governance", "governor"), s.mutatePath("release_trial", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			ExpectedVersion int                        `json:"expected_version"`
			Parameters      map[string]json.RawMessage `json:"parameters"`
		}](b)
		if e != nil {
			return 0, nil, id, e
		}
		row, body, e := releaseRead(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if row.Version != input.ExpectedVersion || row.State != "submitted" || !slices.Contains([]string{"dataflow", "workflow"}, body.Kind) {
			return 0, nil, id, conflict
		}
		if body.TrialRunID != "" {
			var previous GovernanceRun
			if e = tx.Where("id=?", body.TrialRunID).Take(&previous).Error; e != nil {
				return 0, nil, id, e
			}
			if previous.State == "queued" || previous.State == "running" {
				return 0, nil, id, conflict
			}
		}
		trial := governanceTrial{ReleaseID: id, Digest: body.CandidateDigest}
		scoped := workspaceContext(tx, body.WorkspaceID, false).WithContext(context.WithValue(workspaceContext(tx, body.WorkspaceID, false).Statement.Context, governanceTrialContextKey{}, trial))
		var raw json.RawMessage
		var runID string
		if body.Kind == "workflow" {
			_, raw, runID, e = s.enqueueGovernanceWorkflowParameters(scoped, p, body.Candidate, nil, input.Parameters)
		} else {
			_, raw, runID, e = s.enqueueGovernanceFlow(scoped, p, body.Candidate, encode(governanceFlowRunInput{Parameters: input.Parameters}))
		}
		if e != nil {
			return 0, nil, id, e
		}
		body.TrialRunID = runID
		row.Body = encode(body)
		row.Version++
		if e = save(tx, &row); e != nil {
			return 0, nil, id, e
		}
		return 201, raw, id, nil
	}))
	write.POST("/governance/releases/recover", s.require("governance", "governor"), s.mutate("release_recover", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			Kind            string        `json:"kind"`
			Source          GovernanceRef `json:"source"`
			ExpectedVersion int           `json:"expected_version"`
			Reason          string        `json:"reason"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		if !valid(input.Reason, 4000) {
			return 0, nil, "", invalid
		}
		head, source, e := governanceRead(tx, p, input.Kind, input.Source.ID, input.Source.Version)
		if e != nil {
			return 0, nil, "", e
		}
		if source.PublishedAt == nil || head.LatestVersion != input.ExpectedVersion {
			return 0, nil, "", conflict
		}
		definition, e := decode[GovernanceDefinition](source.Definition)
		if e != nil {
			return 0, nil, "", e
		}
		return s.governanceSave(tx, p, input.Kind, head.ID, encode(governanceWrite{Definition: definition, BaseVersion: head.LatestVersion, ChangeNote: "恢复历史引用 v" + fmt.Sprint(source.Version) + "：" + input.Reason, ExecutionMode: source.ExecutionMode}))
	}))
	for _, action := range []string{"approve", "reject", "publish"} {
		action := action
		write.POST("/governance/releases/:id/"+action, s.require("governance", "governor"), s.mutatePath("release_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			input, e := bind[struct {
				ExpectedVersion int    `json:"expected_version"`
				Note            string `json:"note"`
			}](b)
			if e != nil {
				return 0, nil, id, e
			}
			row, body, e := releaseRead(tx, p, id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if row.Version != input.ExpectedVersion {
				return 0, nil, id, conflict
			}
			head, candidate, e := governanceRead(tx, p, body.Kind, body.Candidate.ID, body.Candidate.Version)
			if e != nil {
				return 0, nil, id, e
			}
			if candidate.State != "draft" || head.LatestVersion != candidate.Version || fingerprint(candidate.Definition) != body.CandidateDigest {
				return 0, nil, id, conflict
			}
			if action == "publish" {
				if row.State != "approved" {
					return 0, nil, id, conflict
				}
				status, _, _, e := s.governanceTransition(tx, p, body.Kind, body.Candidate.ID, body.Candidate.Version, "publish", encode(governancePublish{MigrationNote: body.MigrationNote}))
				if e != nil {
					return 0, nil, id, e
				}
				row, _, e = releaseRead(tx, p, id, false)
				return status, governanceCapabilityRecord(row), id, e
			}
			if row.State != "submitted" || !valid(input.Note, 4000) {
				return 0, nil, id, conflict
			}
			if p.UserID == row.OwnerID || p.UserID == candidate.CreatedBy {
				return 0, nil, id, problem(403, "GOVERNANCE_INDEPENDENT_REVIEW", "申请人和版本建立人不能审核自己的变更")
			}
			if action == "approve" {
				if e = s.releaseTrialEvidence(tx, p, body); e != nil {
					return 0, nil, id, e
				}
				row.State = "approved"
			} else {
				row.State = "rejected"
			}
			now := time.Now().UTC()
			body.ReviewerID = p.UserID
			body.ReviewNote = input.Note
			body.ReviewedAt = &now
			row.Body = encode(body)
			row.Version++
			e = save(tx, &row)
			return 200, governanceCapabilityRecord(row), id, e
		}))
	}
}

func governanceValidateTrial(tx *gorm.DB, p identity.Principal, trial governanceTrial, ref GovernanceRef, definition json.RawMessage, dispatch bool) error {
	row, body, e := releaseRead(tx, p, trial.ReleaseID, false)
	if e != nil {
		return e
	}
	if !slices.Contains([]string{"submitted", "approved", "published"}, row.State) || !slices.Contains([]string{"dataflow", "workflow"}, body.Kind) || body.Candidate != ref || body.CandidateDigest != trial.Digest || fingerprint(definition) != trial.Digest {
		return conflict
	}
	if dispatch {
		if row.State != "submitted" {
			return conflict
		}
	} else if body.TrialRunID != trial.RunID || trial.RunID == "" {
		return conflict
	}
	return nil
}

func governanceCapabilityRecord(row Record) json.RawMessage {
	return encode(gin.H{"id": row.ID, "state": row.State, "version": row.Version, "body": row.Body, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt})
}
