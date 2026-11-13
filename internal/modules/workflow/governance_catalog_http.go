package workflow

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceDefinitionRecord struct {
	WorkspaceID   *string   `json:"workspace_id"`
	ID            string    `json:"id" gorm:"primaryKey"`
	Kind          string    `json:"kind"`
	DepartmentID  string    `json:"department_id"`
	LatestVersion int       `json:"latest_version"`
	CreatedAt     time.Time `json:"created_at"`
}

func (GovernanceDefinitionRecord) TableName() string { return "governance_definitions" }

type GovernanceVersion struct {
	ExecutionMode string          `json:"execution_mode" gorm:"default:batch"`
	DefinitionID  string          `json:"id" gorm:"primaryKey"`
	Version       int             `json:"version" gorm:"primaryKey"`
	State         string          `json:"state"`
	Definition    json.RawMessage `json:"definition" gorm:"type:jsonb"`
	ChangeNote    string          `json:"change_note"`
	MigrationNote string          `json:"migration_note"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	PublishedAt   *time.Time      `json:"published_at"`
}

func (GovernanceVersion) TableName() string { return "governance_versions" }

type governanceWrite struct {
	ExecutionMode string               `json:"execution_mode"`
	BaseVersion   int                  `json:"base_version"`
	Definition    GovernanceDefinition `json:"definition"`
	ChangeNote    string               `json:"change_note"`
}
type governancePublish struct {
	MigrationNote string `json:"migration_note"`
}
type governanceReference struct {
	Kind string
	Ref  GovernanceRef
}

func governanceReferences(v GovernanceDefinition) []governanceReference {
	refs := []governanceReference{}
	if v.Scenario != nil {
		refs = append(refs, governanceReference{v.Scenario.Kind, v.Scenario.Reference})
	}
	if v.Master != nil {
		if v.Master.ModelRef != nil {
			refs = append(refs, governanceReference{"model", *v.Master.ModelRef})
		}
		for _, field := range v.Master.Model.Fields {
			if field.Standard != nil {
				refs = append(refs, governanceReference{"standard", *field.Standard})
			}
		}
	}
	if v.EnterpriseDomain != nil {
		refs = append(refs, governanceReference{"domain", *v.EnterpriseDomain})
	}
	if v.Workflow != nil {
		for _, task := range v.Workflow.Tasks {
			if governanceWorkflowTaskKind(task) == "dataflow" {
				refs = append(refs, governanceReference{"dataflow", task.Dataflow})
			}
		}
	}
	if v.Dataflow != nil {
		for _, node := range v.Dataflow.Nodes {
			if node.Template != nil {
				refs = append(refs, governanceReference{"quality_template", *node.Template})
			}
			if node.Model != nil {
				refs = append(refs, governanceReference{"model", *node.Model})
			}
			if node.Code != nil {
				refs = append(refs, governanceReference{"standard", node.Code.Standard})
			}
		}
	}
	if v.Domain != nil {
		refs = append(refs, governanceReference{"domain", *v.Domain})
	}
	if v.Metadata != nil {
		for _, f := range v.Metadata.Fields {
			if f.Standard != nil {
				refs = append(refs, governanceReference{"standard", *f.Standard})
			}
			if f.Term != nil {
				refs = append(refs, governanceReference{"term", *f.Term})
			}
		}
	}
	if v.Model != nil {
		for _, f := range v.Model.Fields {
			if f.Standard != nil {
				refs = append(refs, governanceReference{"standard", *f.Standard})
			}
		}
		for _, r := range v.Model.Relations {
			refs = append(refs, governanceReference{"model", r.Target})
		}
	}
	return refs
}
func governanceRead(tx *gorm.DB, p identity.Principal, kind, id string, version int) (GovernanceDefinitionRecord, GovernanceVersion, error) {
	var head GovernanceDefinitionRecord
	var row GovernanceVersion
	err := tx.Where("id=? AND kind=?", id, kind).Take(&head).Error
	if err != nil {
		return head, row, err
	}
	if e := governanceWorkspaceMatch(tx.Statement.Context, workspaceID(head.WorkspaceID)); e != nil {
		return head, row, e
	}
	if workspaceScope(tx.Statement.Context).Mutation && workspacePrivateKind(kind) {
		if _, e := governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID)); e != nil {
			return head, row, e
		}
	}
	if version == 0 {
		version = head.LatestVersion
		if !admin(p) && kind == "domain" {
			var latest GovernanceVersion
			if err := tx.Where("definition_id=? AND version=?", id, version).Take(&latest).Error; err != nil {
				return head, row, err
			}
			definition, err := decode[GovernanceDefinition](latest.Definition)
			if err != nil {
				return head, row, err
			}
			if definition.DomainScope == "enterprise" {
				if err := tx.Where("definition_id=? AND state='published' AND definition->>'domain_scope'='enterprise'", id).Order("version DESC").Take(&row).Error; err != nil {
					return head, row, denied
				}
				version = row.Version
			}
		}
	}
	err = tx.Where("definition_id=? AND version=?", id, version).Take(&row).Error
	if err != nil {
		return head, row, err
	}
	if !admin(p) {
		definition, e := decode[GovernanceDefinition](row.Definition)
		if e != nil {
			return head, row, e
		}
		if kind == "domain" && definition.DomainScope == "enterprise" {
			if row.State != "published" {
				return head, row, denied
			}
			if e := tx.Model(&GovernanceVersion{}).Where("definition_id=? AND state='published'", id).Select("MAX(version)").Scan(&head.LatestVersion).Error; e != nil {
				return head, row, e
			}
		} else if head.DepartmentID != p.DepartmentID {
			return head, row, denied
		}
	}

	return head, row, err
}
func governanceMember(tx *gorm.DB, id, department string) error {
	var count int64
	err := tx.Table("users u").Where("u.id=? AND u.department_id=? AND u.active", id, department).Where("EXISTS(SELECT 1 FROM user_roles r WHERE r.user_id=u.id AND r.role IN ('governor','admin'))").Where("EXISTS(SELECT 1 FROM user_platform_access a WHERE a.user_id=u.id AND a.platform='governance') OR EXISTS(SELECT 1 FROM user_roles r WHERE r.user_id=u.id AND r.role='admin')").Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return problem(400, "GOVERNANCE_MEMBER_REQUIRED", "责任人和维护人必须是本部门有效的治理人员")
	}
	return nil
}
func governanceValidateReferences(tx *gorm.DB, p identity.Principal, department string, space string, v GovernanceDefinition) error {
	return governanceValidateReferencesForPreview(tx, p, department, space, v, "")
}
func governanceValidateReferencesForPreview(tx *gorm.DB, p identity.Principal, department, space string, v GovernanceDefinition, preview string) error {
	for _, id := range []string{v.OwnerID, v.StewardID} {
		if err := governanceMember(tx, id, department); err != nil {
			return err
		}
	}
	if v.Metadata != nil {
		if err := governanceMetadataReference(tx, p, department, *v.Metadata); err != nil {
			return err
		}
	}
	if v.Workflow != nil {
		if _, e := governanceWorkflowParameters(tx, p, *v.Workflow, nil); e != nil {
			return e
		}
	}
	if space != "" && v.Dataflow != nil {
		for _, node := range v.Dataflow.Nodes {
			if node.Source != nil {
				if err := governanceWorkspaceReference(tx, p, space, node.Source.Resource); err != nil {
					return err
				}
			}
		}
	}
	resolved := map[GovernanceRef]GovernanceDefinition{}
	for _, ref := range governanceReferences(v) {
		head, row, err := governanceRead(tx, p, ref.Kind, ref.Ref.ID, ref.Ref.Version)
		if err != nil {
			return err
		}
		if workspacePrivateKind(ref.Kind) && workspaceID(head.WorkspaceID) != space {
			return problem(409, "GOVERNANCE_WORKSPACE_REFERENCE", "引用的模型或处理任务属于其他空间，请在本空间建立独立定义")
		}
		definition, err := decode[GovernanceDefinition](row.Definition)
		if err != nil {
			return err
		}
		enterprise := ref.Kind == "domain" && definition.DomainScope == "enterprise"
		if v.EnterpriseDomain != nil && ref.Ref == *v.EnterpriseDomain && !enterprise {
			return problem(400, "GOVERNANCE_ENTERPRISE_DOMAIN_REQUIRED", "部门责任必须关联企业统一主题域")
		}
		if head.DepartmentID != department && !enterprise {
			return denied
		}
		// A shared version cannot retire between reference validation and commit.
		if enterprise {
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("definition_id=? AND version=?", head.ID, row.Version).Take(&row).Error; err != nil {
				return err
			}
		}
		if row.State != "published" {
			return problem(409, "GOVERNANCE_REFERENCE_UNAVAILABLE", "引用必须指向本部门已发布且未停用的明确版本")
		}
		resolved[ref.Ref] = definition
	}
	if v.Workflow != nil {
		for _, task := range v.Workflow.Tasks {
			if governanceWorkflowTaskKind(task) != "dataflow" {
				continue
			}
			flow := resolved[task.Dataflow].Dataflow
			if flow == nil {
				return invalid
			}
			for node := range task.Inputs {
				if !slices.ContainsFunc(flow.Nodes, func(candidate GovernanceFlowNode) bool { return candidate.ID == node && candidate.Kind == "source" }) {
					return problem(400, "WORKFLOW_ARTIFACT_NODE", "产物绑定必须指向任务数据流中的来源节点")
				}
			}
		}
	}
	if v.Model != nil {
		fields := map[string]GovernanceField{}
		for _, f := range v.Model.Fields {
			fields[f.Name] = f
			if f.Standard == nil {
				continue
			}
			standard := resolved[*f.Standard].Standard
			if standard == nil {
				return invalid
			}
			if f.LogicalType != standard.LogicalType || (f.Nullable && !standard.Nullable) || (standard.Sensitive && !f.Sensitive) || (standard.Length > 0 && (f.Length == 0 || f.Length > standard.Length)) || (f.LogicalType == "decimal" && (f.Precision > standard.Precision || f.Scale != standard.Scale)) {
				return problem(400, "MODEL_STANDARD_CONFLICT", "模型字段的类型、长度、精度、空值或敏感性与引用标准冲突")
			}
		}
		for _, relation := range v.Model.Relations {
			target := resolved[relation.Target].Model
			if target == nil {
				return invalid
			}
			targetFields := map[string]GovernanceField{}
			targetKeys := []string{}
			sourceKeys := []string{}
			for _, f := range target.Fields {
				targetFields[f.Name] = f
				if f.PrimaryKey {
					targetKeys = append(targetKeys, f.Name)
				}
			}
			for _, f := range v.Model.Fields {
				if f.PrimaryKey {
					sourceKeys = append(sourceKeys, f.Name)
				}
			}
			for i, name := range relation.Fields {
				a := fields[name]
				b, ok := targetFields[relation.TargetFields[i]]
				if !ok || a.LogicalType != b.LogicalType || a.Precision != b.Precision || a.Scale != b.Scale {
					return problem(400, "MODEL_RELATION_CONFLICT", "模型关系的字段类型或目标字段不匹配")
				}
			}
			same := func(a, b []string) bool {
				x := slices.Clone(a)
				y := slices.Clone(b)
				slices.Sort(x)
				slices.Sort(y)
				return slices.Equal(x, y)
			}
			if (relation.Cardinality != "one_to_many" && !same(relation.TargetFields, targetKeys)) || (relation.Cardinality != "many_to_one" && !same(relation.Fields, sourceKeys)) {
				return problem(400, "MODEL_RELATION_KEY_REQUIRED", "关系的一端必须引用完整主键，与所选基数保持一致")
			}
		}
	}
	if v.Dataflow != nil {
		sources := map[string][]Field{}
		models := map[GovernanceRef]GovernanceModel{}
		standards := map[GovernanceRef]GovernanceStandard{}
		for ref, definition := range resolved {
			if definition.Model != nil {
				models[ref] = *definition.Model
			}
			if definition.Standard != nil {
				standards[ref] = *definition.Standard
			}
		}
		for _, node := range v.Dataflow.Nodes {
			if node.Source == nil {
				continue
			}
			row, target, e := metadataTarget(tx, p, node.Source.Resource.ID, false)
			if e != nil {
				return e
			}
			if row.Kind == "dataset" && row.State == "retired" {
				return problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "成果已清理，请选择保留中的成果")
			}
			if row.Version != node.Source.Resource.Version || target.ResourceType != node.Source.Resource.Type {
				return problem(409, "GOVERNANCE_INPUT_CHANGED", "来源资源契约已变化")
			}
			sources[node.ID] = target.Fields
		}
		templates := map[GovernanceRef]GovernanceQualityTemplate{}
		for ref, definition := range resolved {
			if definition.QualityTemplate != nil {
				templates[ref] = *definition.QualityTemplate
			}
		}
		graph, err := governanceTemplateGraph(*v.Dataflow, templates)
		if err != nil {
			return err
		}
		plan, err := compileGovernanceFlow(graph, sources, models, standards, preview)
		if err != nil {
			return err
		}
		if err = governanceTemplatePlan(graph, templates, plan); err != nil {
			return err
		}
	}
	return nil
}

// Serializing low-volume definition mutations per department makes publish/retire
// and reference checks atomic without holding locks over external data sources.
func governanceLock(tx *gorm.DB, department string) error {
	if e := tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return e
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "governance:"+department).Error
}
func governanceVersionResponse(head GovernanceDefinitionRecord, row GovernanceVersion) json.RawMessage {
	return encode(gin.H{"id": head.ID, "kind": head.Kind, "department_id": head.DepartmentID, "workspace_id": head.WorkspaceID, "latest_version": head.LatestVersion, "version": row.Version, "state": row.State, "execution_mode": row.ExecutionMode, "definition": row.Definition, "change_note": row.ChangeNote, "migration_note": row.MigrationNote, "created_at": row.CreatedAt, "published_at": row.PublishedAt})
}
func (s *Server) registerGovernanceCatalog(api, write *gin.RouterGroup) {
	s.registerGovernanceWorkspaces(api, write)
	s.registerGovernanceMonitoring(api, write)
	s.registerGovernanceReleases(api, write)
	s.registerGovernanceCapabilities(api, write)
	s.registerGovernanceStandardCoverage(api, write)
	s.registerGovernanceModelDeployments(api, write)
	s.registerGovernanceModelLabels(api)
	api.GET("/governance/members", s.require("governance", "governor"), s.governanceMembers)
	s.registerGovernanceMetadata(api, write)
	s.registerGovernanceRuns(api, write)
	s.registerGovernanceWorkflows(api, write)
	s.registerGovernanceOperations(api, write)
	for _, item := range []struct{ Path, Kind string }{{"masters", "master"}, {"scenarios", "scenario"}, {"workflows", "workflow"}, {"dataflows", "dataflow"}, {"domains", "domain"}, {"terms", "term"}, {"standards", "standard"}, {"models", "model"}, {"metadata-definitions", "metadata"}, {"quality-templates", "quality_template"}} {
		kind := item.Kind
		path := "/governance/" + item.Path
		api.GET(path, s.require("governance", "governor"), s.governanceList(kind))
		api.GET(path+"/:id/versions", s.require("governance", "governor"), s.governanceHistory(kind))
		api.GET(path+"/:id/versions/:version", s.require("governance", "governor"), s.governanceDetail(kind))
		api.GET(path+"/:id/diff", s.require("governance", "governor"), s.governanceDiff(kind))
		api.GET(path+"/:id/impact", s.require("governance", "governor"), s.governanceImpact(kind))
		write.POST(path, s.require("governance", "governor"), s.mutate("governance_create_"+kind, func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			return s.governanceSave(tx, p, kind, "", b)
		}))
		write.POST(path+"/:id/versions", s.require("governance", "governor"), s.mutatePath("governance_version_"+kind, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			return s.governanceSave(tx, p, kind, id, b)
		}))
		for _, state := range []string{"publish", "retire"} {
			action := state
			write.POST(path+"/:id/versions/:version/"+action, s.require("governance", "governor"), func(c *gin.Context) {
				version, err := strconv.Atoi(c.Param("version"))
				if err != nil || version < 1 {
					s.fail(c, invalid)
					return
				}
				id := c.Param("id")
				s.mutate("governance_"+action+"_"+kind, func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
					return s.governanceTransition(tx, p, kind, id, version, action, b)
				})(c)
			})
		}
	}
	api.GET("/governance/models/:id/versions/:version/ddl", s.require("governance", "governor"), s.governanceModelDDL)
}
func (s *Server) governanceSave(tx *gorm.DB, p identity.Principal, kind, id string, b []byte) (int, json.RawMessage, string, error) {
	input, err := bind[governanceWrite](b)
	if err != nil {
		return 0, nil, "", err
	}
	if (input.ExecutionMode != "" && kind != "dataflow") || (input.ExecutionMode != "" && input.ExecutionMode != "batch" && input.ExecutionMode != "continuous") {
		return 0, nil, "", invalid
	}
	if len(input.ChangeNote) > 4000 {
		return 0, nil, "", invalid
	}
	if input.Definition.Metadata != nil && input.Definition.Metadata.Fields == nil {
		input.Definition.Metadata.Fields = []GovernanceFieldAnnotation{}
	}
	if input.Definition.Standard != nil && input.Definition.Standard.Values == nil {
		input.Definition.Standard.Values = []string{}
	}
	if input.Definition.Model != nil {
		if input.Definition.Model.Fields == nil {
			input.Definition.Model.Fields = []GovernanceField{}
		}
		if input.Definition.Model.Relations == nil {
			input.Definition.Model.Relations = []GovernanceRelation{}
		}
	}
	if input.Definition.Standard != nil && len(input.Definition.Standard.Values) > 0 && input.Definition.Standard.ValueMeanings == nil {
		return 0, nil, "", problem(400, "GOVERNANCE_CODE_MEANING_REQUIRED", "请为每个码值补充业务含义；历史版本可继续读取和引用")
	}
	if input.Definition.Master != nil && input.Definition.Master.ModelRef != nil {
		ref := input.Definition.Master.ModelRef
		_, model, e := governanceRead(tx, p, "model", ref.ID, ref.Version)
		if e != nil {
			return 0, nil, id, e
		}
		if model.State != "published" {
			return 0, nil, id, conflict
		}
		definition, e := decode[GovernanceDefinition](model.Definition)
		if e != nil || definition.Model == nil {
			return 0, nil, id, invalid
		}
		input.Definition.Master.Model = *definition.Model
	}

	if err = validateGovernanceDefinition(kind, input.Definition); err != nil {
		return 0, nil, "", err
	}
	department := p.DepartmentID
	if id == "" && input.Definition.Metadata != nil {
		record, _, err := metadataTarget(tx, p, input.Definition.Metadata.ObjectID, false)
		if err != nil {
			return 0, nil, "", err
		}
		department = record.DepartmentID
	}
	var head GovernanceDefinitionRecord
	if id != "" {
		var previous GovernanceVersion
		head, previous, err = governanceRead(tx, p, kind, id, 0)
		if err != nil {
			return 0, nil, "", err
		}
		department = head.DepartmentID
		if input.ExecutionMode == "" {
			input.ExecutionMode = previous.ExecutionMode
		}
	}
	if kind == "domain" {
		if input.Definition.DomainScope == "enterprise" && !admin(p) {
			return 0, nil, "", denied
		}
		if id != "" {
			_, old, e := governanceRead(tx, p, kind, id, 0)
			if e != nil {
				return 0, nil, "", e
			}
			before, e := decode[GovernanceDefinition](old.Definition)
			if e != nil {
				return 0, nil, "", e
			}
			if before.DomainScope == "enterprise" && !admin(p) {
				return 0, nil, "", denied
			}
			if (before.DomainScope == "enterprise") != (input.Definition.DomainScope == "enterprise") {
				return 0, nil, "", problem(409, "GOVERNANCE_DOMAIN_SCOPE_IMMUTABLE", "主题域范围不能通过修订改变，请另建统一定义并关联部门责任")
			}
		}
	}
	if workspacePrivateKind(kind) {
		var space GovernanceWorkspace
		if id == "" {
			space, err = governanceWorkspaceCreateScope(tx, p, department)
		} else {
			space, err = governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID))
		}
		if err != nil {
			return 0, nil, "", err
		}
		if id == "" {
			head.WorkspaceID = &space.ID
		}
	}
	if err = governanceLock(tx, department); err != nil {
		return 0, nil, "", err
	}
	if kind == "domain" && input.Definition.EnterpriseDomain != nil {
		var count int64
		if err := tx.Table("governance_definitions d").Joins("JOIN governance_versions v ON v.definition_id=d.id AND v.version=d.latest_version").Where("d.kind='domain' AND d.department_id=? AND d.id<>? AND v.definition->'enterprise_domain'->>'id'=?", department, id, input.Definition.EnterpriseDomain.ID).Count(&count).Error; err != nil {
			return 0, nil, "", err
		}
		if count > 0 {
			return 0, nil, "", problem(409, "GOVERNANCE_RESPONSIBILITY_EXISTS", "本部门已有该企业主题域的责任分工，请修订已有定义")
		}
	}
	if id == "" {
		if input.BaseVersion != 0 {
			return 0, nil, "", invalid
		}
		id, err = newID()
		if err != nil {
			return 0, nil, "", err
		}
		head = GovernanceDefinitionRecord{ID: id, Kind: kind, DepartmentID: department, WorkspaceID: head.WorkspaceID, LatestVersion: 1}
	}
	if err = s.capabilityReferences(tx, p, workspaceID(head.WorkspaceID), &input.Definition); err != nil {
		return 0, nil, id, err
	}
	if err = governanceValidateReferences(tx, p, department, workspaceID(head.WorkspaceID), input.Definition); err != nil {
		return 0, nil, "", err
	}
	if input.BaseVersion > 0 {
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND kind=?", id, kind).Take(&head).Error; err != nil {
			return 0, nil, "", err
		}
		if head.LatestVersion != input.BaseVersion {
			return 0, nil, "", problem(409, "GOVERNANCE_VERSION_CONFLICT", "定义已产生新版本，请刷新后对比再保存")
		}
		head.LatestVersion++
		if err = tx.Model(&head).Update("latest_version", head.LatestVersion).Error; err != nil {
			return 0, nil, "", err
		}
	} else if head.CreatedAt.IsZero() {
		if err = tx.Create(&head).Error; err != nil {
			return 0, nil, "", err
		}
	} else {
		return 0, nil, "", invalid
	}
	if input.ExecutionMode == "" {
		input.ExecutionMode = "batch"
	}
	row := GovernanceVersion{ExecutionMode: input.ExecutionMode, DefinitionID: id, Version: head.LatestVersion, State: "draft", Definition: encode(input.Definition), ChangeNote: input.ChangeNote, CreatedBy: p.UserID}
	if err = tx.Create(&row).Error; err != nil {
		return 0, nil, "", err
	}
	return 201, governanceVersionResponse(head, row), id, nil
}
func governanceDependents(tx *gorm.DB, department, id string, version int) ([]GovernanceVersion, error) {
	rows := []GovernanceVersion{}
	ref := GovernanceRef{ID: id, Version: version}
	err := tx.Table("governance_versions v").Select("v.*").Joins("JOIN governance_definitions d ON d.id=v.definition_id").Where("(?='' OR d.department_id=?) AND v.state='published'", department, department).Where("v.definition @> ?::jsonb OR v.definition @> ?::jsonb OR v.definition->'model'->'fields' @> ?::jsonb OR v.definition->'model'->'relations' @> ?::jsonb OR v.definition->'metadata'->'fields' @> ?::jsonb OR v.definition->'metadata'->'fields' @> ?::jsonb OR v.definition->'dataflow'->'nodes' @> ?::jsonb OR v.definition->'dataflow'->'nodes' @> ?::jsonb OR v.definition->'workflow'->'tasks' @> ?::jsonb OR v.definition @> ?::jsonb OR v.definition @> ?::jsonb", string(encode(gin.H{"domain": ref})), string(encode(gin.H{"enterprise_domain": ref})), string(encode([]gin.H{{"standard": ref}})), string(encode([]gin.H{{"target": ref}})), string(encode([]gin.H{{"standard": ref}})), string(encode([]gin.H{{"term": ref}})), string(encode([]gin.H{{"model": ref}})), string(encode([]gin.H{{"code": gin.H{"standard": ref}}})), string(encode([]gin.H{{"dataflow": ref}})), string(encode(gin.H{"master": gin.H{"model_ref": ref}})), string(encode(gin.H{"scenario": gin.H{"reference": ref}}))).Order("v.definition_id,v.version").Find(&rows).Error
	return rows, err
}
func (s *Server) governanceTransition(tx *gorm.DB, p identity.Principal, kind, id string, version int, action string, b []byte) (int, json.RawMessage, string, error) {
	input, err := bind[governancePublish](b)
	if err != nil || len(input.MigrationNote) > 4000 {
		return 0, nil, "", invalid
	}
	head, row, err := governanceRead(tx, p, kind, id, version)
	if err != nil {
		return 0, nil, "", err
	}
	if workspacePrivateKind(kind) {
		if _, err = governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID)); err != nil {
			return 0, nil, "", err
		}
	}
	if err = governanceLock(tx, head.DepartmentID); err != nil {
		return 0, nil, "", err
	}
	head, row, err = governanceRead(tx, p, kind, id, version)
	if err != nil {
		return 0, nil, "", err
	}
	definitionScope, err := decode[GovernanceDefinition](row.Definition)
	if err != nil {
		return 0, nil, "", err
	}
	enterprise := kind == "domain" && definitionScope.DomainScope == "enterprise"
	if enterprise {
		if !admin(p) {
			return 0, nil, "", denied
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("definition_id=? AND version=?", id, version).Take(&row).Error; err != nil {
			return 0, nil, "", err
		}
	}
	if action == "publish" {
		if err = s.governanceReleaseGate(tx, p, kind, id, version, row.Definition); err != nil {
			return 0, nil, id, err
		}
		if row.State != "draft" || head.LatestVersion != version {
			return 0, nil, "", conflict
		}
		definition, err := decode[GovernanceDefinition](row.Definition)
		if err != nil {
			return 0, nil, "", err
		}
		if definition.Dataflow != nil {
			if err = s.validateGovernanceScriptRuntimes(tx.Statement.Context, *definition.Dataflow); err != nil {
				return 0, nil, "", err
			}
		}
		if err = s.capabilityReferences(tx, p, workspaceID(head.WorkspaceID), &definition); err != nil {
			return 0, nil, id, err
		}
		if err = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), definition); err != nil {
			return 0, nil, "", err
		}
		if kind == "standard" {
			var duplicates int64
			if err = tx.Table("governance_versions v").Joins("JOIN governance_definitions d ON d.id=v.definition_id").Where("d.department_id=? AND d.kind='standard' AND d.id<>? AND v.state='published' AND v.definition->'standard'->>'code'=?", head.DepartmentID, id, definition.Standard.Code).Count(&duplicates).Error; err != nil {
				return 0, nil, "", err
			}
			if duplicates > 0 {
				return 0, nil, "", problem(409, "GOVERNANCE_CODE_CONFLICT", "本部门已有其他已发布标准使用此编码，请修改编码或修订原标准")
			}
		}
		var previous GovernanceVersion
		err = tx.Where("definition_id=? AND version<? AND published_at IS NOT NULL", id, version).Order("version DESC").Limit(1).Find(&previous).Error
		if err != nil {
			return 0, nil, "", err
		}
		if previous.Version > 0 {
			before, err := decode[GovernanceDefinition](previous.Definition)
			if err != nil {
				return 0, nil, "", err
			}
			for _, change := range governanceChanges(kind, before, definition) {
				if change.Breaking && !valid(input.MigrationNote, 4000) {
					return 0, nil, "", problem(409, "MODEL_MIGRATION_REQUIRED", "存在破坏性变更，请填写下游迁移方案后发布")
				}
			}
		}
		now := time.Now().UTC()
		row.State = "published"
		row.PublishedAt = &now
		row.MigrationNote = input.MigrationNote
	} else {
		if row.State != "published" {
			return 0, nil, "", conflict
		}
		if err := governanceCanRetire(tx, kind, id, version); err != nil {
			return 0, nil, id, err
		}
		dependencyDepartment := head.DepartmentID
		if enterprise {
			dependencyDepartment = ""
		}
		dependents, err := governanceDependents(tx, dependencyDepartment, id, version)
		if err != nil {
			return 0, nil, "", err
		}
		if len(dependents) > 0 {
			return 0, nil, "", problem(409, "GOVERNANCE_VERSION_IN_USE", "此版本仍被已发布定义引用，请先处理下游版本")
		}
		row.State = "retired"
	}
	if err = tx.Save(&row).Error; err != nil {
		return 0, nil, "", err
	}
	return 200, governanceVersionResponse(head, row), id, nil
}
func (s *Server) governanceList(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, err := intQuery(c, "page", 1, 100000)
		if err != nil {
			s.fail(c, err)
			return
		}
		size, err := intQuery(c, "page_size", 20, 100)
		if err != nil {
			s.fail(c, err)
			return
		}
		p := principal(c)
		state := c.Query("state")
		if state != "" && state != "published" {
			s.fail(c, invalid)
			return
		}
		join := "JOIN governance_versions v ON v.definition_id=d.id AND v.version=d.latest_version"
		if state == "published" {
			join = "JOIN governance_versions v ON v.definition_id=d.id AND v.state='published'"
		}
		if kind == "domain" && !admin(p) && state == "" {
			join = "JOIN governance_versions v ON v.definition_id=d.id AND (v.version=d.latest_version OR (v.definition->>'domain_scope'='enterprise' AND v.state='published' AND v.version=(SELECT MAX(pv.version) FROM governance_versions pv WHERE pv.definition_id=d.id AND pv.state='published' AND pv.definition->>'domain_scope'='enterprise')))"
		}
		q := s.db.WithContext(c.Request.Context()).Table("governance_definitions d").Joins(join).Where("d.kind=?", kind)
		if purpose := c.Query("purpose"); purpose != "" {
			if purpose != "metric" || kind != "dataflow" {
				s.fail(c, invalid)
				return
			}
			q = q.Where("v.definition->'metric' IS NOT NULL AND v.definition->'metric'<>'null'::jsonb")
		}
		if workspacePrivateKind(kind) {
			var e error
			q, e = s.governanceWorkspaceFilter(c, q, "d.workspace_id", true)
			if e != nil {
				s.fail(c, e)
				return
			}
		}
		if !admin(p) {
			if kind == "domain" {
				q = q.Where("(d.department_id=? AND COALESCE(v.definition->>'domain_scope','department')<>'enterprise' AND (?='published' OR v.version=d.latest_version)) OR (v.definition->>'domain_scope'='enterprise' AND v.state='published' AND (?='published' OR v.version=(SELECT MAX(pv.version) FROM governance_versions pv WHERE pv.definition_id=d.id AND pv.state='published')))", p.DepartmentID, state, state)
			} else {
				q = q.Where("d.department_id=?", p.DepartmentID)
			}
		}
		if layer := c.Query("data_layer"); layer != "" {
			if kind != "model" || !validDataLayer(layer) {
				s.fail(c, invalid)
				return
			}
			q = q.Where("v.definition->'model'->>'data_layer'=?", layer)
		}
		if scope := c.Query("domain_scope"); scope != "" {
			if kind != "domain" || !slices.Contains([]string{"enterprise", "department"}, scope) {
				s.fail(c, invalid)
				return
			}
			q = q.Where("COALESCE(v.definition->>'domain_scope','department')=?", scope)
		}
		if targetDepartment := c.Query("department_id"); targetDepartment != "" {
			if kind != "domain" || !valid(targetDepartment, 64) {
				s.fail(c, invalid)
				return
			}
			if !admin(p) && targetDepartment != p.DepartmentID {
				s.fail(c, denied)
				return
			}
			q = q.Where("d.department_id=?", targetDepartment)
		}
		if enterpriseID := c.Query("enterprise_domain_id"); enterpriseID != "" {
			if kind != "domain" || !valid(enterpriseID, 64) {
				s.fail(c, invalid)
				return
			}
			q = q.Where("v.definition->'enterprise_domain'->>'id'=?", enterpriseID)
		}
		if objectID := c.Query("object_id"); objectID != "" {
			if kind != "metadata" || !valid(objectID, 128) {
				s.fail(c, invalid)
				return
			}
			q = q.Where("v.definition->'metadata'->>'object_id'=?", objectID)
		}
		search := c.Query("search")
		if len(search) > 200 {
			s.fail(c, invalid)
			return
		}
		if search != "" {
			q = q.Where("v.definition->>'name' ILIKE ?", "%"+search+"%")
		}
		if mode := c.Query("execution_mode"); mode != "" {
			if kind != "dataflow" || (mode != "batch" && mode != "continuous") {
				s.fail(c, invalid)
				return
			}
			q = q.Where("v.execution_mode=?", mode)
		}
		var count int64
		if err = q.Count(&count).Error; err != nil {
			s.fail(c, err)
			return
		}
		var rows []struct {
			GovernanceVersion
			Kind          string
			DepartmentID  string
			LatestVersion int
			WorkspaceID   *string
		}

		if err = q.Select("v.*,d.kind,d.department_id,d.workspace_id,CASE WHEN d.kind='domain' AND v.definition->>'domain_scope'='enterprise' AND NOT ? THEN (SELECT MAX(pv.version) FROM governance_versions pv WHERE pv.definition_id=d.id AND pv.state='published') ELSE d.latest_version END AS latest_version", admin(p)).Order("d.created_at DESC,d.id ASC,v.version DESC").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; err != nil {
			s.fail(c, err)
			return
		}
		items := []json.RawMessage{}
		for _, row := range rows {
			items = append(items, governanceVersionResponse(GovernanceDefinitionRecord{ID: row.DefinitionID, Kind: row.Kind, DepartmentID: row.DepartmentID, WorkspaceID: row.WorkspaceID, LatestVersion: row.LatestVersion}, row.GovernanceVersion))
		}
		c.JSON(200, gin.H{"items": items, "total": count, "page": page, "page_size": size})
	}
}
func (s *Server) governanceDetail(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		version, err := strconv.Atoi(c.Param("version"))
		if err != nil || version < 1 {
			s.fail(c, invalid)
			return
		}
		head, row, err := governanceRead(s.db.WithContext(c.Request.Context()), principal(c), kind, c.Param("id"), version)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.Data(200, "application/json", governanceVersionResponse(head, row))
	}
}
func (s *Server) governanceHistory(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		head, _, err := governanceRead(tx, principal(c), kind, c.Param("id"), 0)
		if err != nil {
			s.fail(c, err)
			return
		}
		page, err := intQuery(c, "page", 1, 100000)
		if err != nil {
			s.fail(c, err)
			return
		}
		size, err := intQuery(c, "page_size", 20, 100)
		if err != nil {
			s.fail(c, err)
			return
		}
		query := tx.Where("definition_id=?", head.ID)
		if !admin(principal(c)) && kind == "domain" {
			query = query.Where("state='published' OR COALESCE(definition->>'domain_scope','department')<>'enterprise'")
		}
		var total int64
		if err := query.Model(&GovernanceVersion{}).Count(&total).Error; err != nil {
			s.fail(c, err)
			return
		}
		rows := []GovernanceVersion{}
		if err = query.Order("version DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
			s.fail(c, err)
			return
		}
		items := []json.RawMessage{}
		for _, row := range rows {
			items = append(items, governanceVersionResponse(head, row))
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	}
}
func (s *Server) governanceDiff(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, err := intQuery(c, "from", 1, 1000000)
		if err != nil {
			s.fail(c, err)
			return
		}
		to, err := intQuery(c, "to", 1, 1000000)
		if err != nil {
			s.fail(c, err)
			return
		}
		tx := s.db.WithContext(c.Request.Context())
		_, a, err := governanceRead(tx, principal(c), kind, c.Param("id"), from)
		if err != nil {
			s.fail(c, err)
			return
		}
		_, b, err := governanceRead(tx, principal(c), kind, c.Param("id"), to)
		if err != nil {
			s.fail(c, err)
			return
		}
		before, err := decode[GovernanceDefinition](a.Definition)
		if err != nil {
			s.fail(c, err)
			return
		}
		after, err := decode[GovernanceDefinition](b.Definition)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, gin.H{"from": from, "to": to, "changes": governanceChanges(kind, before, after), "before": before, "after": after})
	}
}
func (s *Server) governanceImpact(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		version, err := intQuery(c, "version", 1, 1000000)
		if err != nil {
			s.fail(c, err)
			return
		}
		tx := s.db.WithContext(c.Request.Context())
		head, _, err := governanceRead(tx, principal(c), kind, c.Param("id"), version)
		if err != nil {
			s.fail(c, err)
			return
		}
		dependencyDepartment := head.DepartmentID
		if kind == "domain" {
			dependencyDepartment = principal(c).DepartmentID
			if admin(principal(c)) {
				dependencyDepartment = ""
			}
		}
		rows, err := governanceDependents(tx, dependencyDepartment, head.ID, version)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": len(rows)})
	}
}
func (s *Server) governanceModelDDL(c *gin.Context) {
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		s.fail(c, invalid)
		return
	}
	_, row, err := governanceRead(s.db.WithContext(c.Request.Context()), principal(c), "model", c.Param("id"), version)
	if err != nil {
		s.fail(c, err)
		return
	}
	definition, err := decode[GovernanceDefinition](row.Definition)
	if err != nil {
		s.fail(c, err)
		return
	}
	ddl, err := governanceDDL(definition, c.DefaultQuery("dialect", "postgres"))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"ddl": ddl, "executed": false, "scope": "columns_and_primary_key", "relations": definition.Model.Relations})
}
func (s *Server) governanceMembers(c *gin.Context) {
	p := principal(c)
	var rows []struct {
		UserID       string `json:"user_id"`
		Username     string `json:"username"`
		DepartmentID string `json:"department_id"`
	}
	q := s.db.WithContext(c.Request.Context()).Table("users u").Select("u.id AS user_id,u.username,u.department_id").Where("u.active").Where("EXISTS(SELECT 1 FROM user_roles r WHERE r.user_id=u.id AND r.role IN ('governor','admin'))").Where("EXISTS(SELECT 1 FROM user_platform_access a WHERE a.user_id=u.id AND a.platform='governance') OR EXISTS(SELECT 1 FROM user_roles r WHERE r.user_id=u.id AND r.role='admin')")
	if !admin(p) {
		q = q.Where("u.department_id=?", p.DepartmentID)
	}
	if err := q.Order("u.username,u.id").Limit(1000).Scan(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	if rows == nil {
		rows = make([]struct {
			UserID       string `json:"user_id"`
			Username     string `json:"username"`
			DepartmentID string `json:"department_id"`
		}, 0)
	}
	c.JSON(200, gin.H{"items": rows})
}
