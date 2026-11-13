package workflow

import (
	"cmp"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"path/filepath"
	"slices"
	"strings"
)

type securityPublished struct {
	ID         string
	Kind       string
	Version    int
	Definition json.RawMessage
}

func securityPublishedControls(tx *gorm.DB, kinds []string, resources ...Resource) ([]securityPublished, error) {
	rows := []securityPublished{}
	q := tx.Table("security_controls h").Select("h.id,h.kind,v.version,v.definition").Joins("JOIN security_control_versions v ON v.control_id=h.id AND v.version=h.published_version AND v.state='published'").Where("h.kind IN ?", kinds)
	if len(resources) > 0 {
		q = q.Where("(v.definition->'resource'=?::jsonb OR (h.kind='policy' AND COALESCE(v.definition->'resource','null'::jsonb)='null'::jsonb))", string(encode(resources[0])))
	}
	e := q.Order("h.id").Limit(1001).Scan(&rows).Error
	if e == nil && len(rows) > 1000 {
		return nil, problem(503, "SECURITY_POLICY_LIMIT", "安全配置超过本次核对上限，请整理后重试")
	}
	return rows, e
}

type SecurityDecision struct {
	Level        string              `json:"level"`
	MaskFields   []string            `json:"mask_fields"`
	RequireProof bool                `json:"require_proof"`
	Visible      bool                `json:"visible"`
	Invisible    bool                `json:"invisible"`
	RowFilters   []SecurityRowFilter `json:"row_filters"`
	FileMark     bool                `json:"file_mark"`
	Text         string              `json:"text"`
	Controls     []GovernanceRef     `json:"controls"`
}

func securityApplies(d SecurityDefinition, ref Resource, level string, p identity.Principal, action string) bool {
	if d.Resource != nil && *d.Resource != ref {
		return false
	}
	if d.Level != "" && d.Level != level {
		return false
	}
	if !slices.Contains(d.Actions, action) {
		return false
	}
	if d.Department != "" && d.Department != p.DepartmentID {
		return false
	}
	if len(d.Roles) > 0 && !slices.ContainsFunc(d.Roles, func(r string) bool { return slices.Contains(p.Roles, r) }) {
		return false
	}
	return true
}
func (s *Server) securityPolicyCheck(tx *gorm.DB, p identity.Principal, ref Resource, action string) (SecurityDecision, error) {
	var active bool
	if e := tx.Raw("SELECT EXISTS(SELECT 1 FROM security_controls WHERE kind IN ('classification','policy') AND published_version>0) OR EXISTS(SELECT 1 FROM security_incidents WHERE state='frozen')").Scan(&active).Error; e != nil {
		return SecurityDecision{}, e
	}
	if !active {
		return SecurityDecision{Level: "unclassified", MaskFields: []string{}, Controls: []GovernanceRef{}, RowFilters: []SecurityRowFilter{}}, nil
	}
	return s.securityPolicyCheckPath(tx, p, ref, action, nil)
}
func (s *Server) securityPolicyCheckPath(tx *gorm.DB, p identity.Principal, ref Resource, action string, path []Resource) (SecurityDecision, error) {
	out := SecurityDecision{Level: "unclassified", MaskFields: []string{}, Controls: []GovernanceRef{}, RowFilters: []SecurityRowFilter{}}
	if len(path) >= 16 || slices.Contains(path, ref) {
		return out, problem(409, "SECURITY_LINEAGE_LIMIT", "来源安全依赖循环或层级过深，须先核对来源")
	}
	path = append(slices.Clone(path), ref)
	if e := securityResourceFrozen(tx, ref); e != nil {
		return out, e
	}
	var stale int64
	if e := tx.Table("security_controls h").Joins("JOIN security_control_versions v ON v.control_id=h.id AND v.version=h.published_version AND v.state='published'").Where("h.kind IN ('classification','policy') AND v.definition->'resource'->>'resource_id'=? AND v.definition->'resource'->>'resource_type'=? AND v.definition->'resource'->>'contract_version'<>?", ref.ID, ref.Type, string(encode(ref.Version))).Count(&stale).Error; e != nil {
		return out, e
	}
	if stale > 0 {
		if _, _, e := resource(tx, ref, false); e != nil {
			return out, e
		}
		out.Level = "pending_review"
		return out, problem(403, "SECURITY_VERSION_REVIEW_REQUIRED", "此资源的新版本尚未完成安全复核，请为原有标签和资源策略更新版本或明确停用旧范围")
	}
	rows, e := securityPublishedControls(tx, []string{"classification", "policy"}, ref)
	if e != nil {
		return out, e
	}
	for _, row := range rows {
		if row.Kind != "classification" {
			continue
		}
		d, err := decode[SecurityDefinition](row.Definition)
		if err != nil {
			return out, err
		}
		if d.Resource == nil || *d.Resource != ref {
			continue
		}
		out.Controls = append(out.Controls, GovernanceRef{ID: row.ID, Version: row.Version})
		if slices.Index(securityLevels, d.Level) > slices.Index(securityLevels, out.Level) {
			out.Level = d.Level
		}
		if (d.Level == "sensitive" || d.Level == "restricted") && d.Field == "" {
			_, schema, err := resource(tx, ref, false)
			if err != nil {
				return out, err
			}
			for _, field := range schema {
				if field.Sensitive {
					out.MaskFields = append(out.MaskFields, field.Name)
				}
			}
		}
		if (d.Level == "sensitive" || d.Level == "restricted") && d.Field != "" {
			out.MaskFields = append(out.MaskFields, d.Field)
		}
	}
	if out.Level == "restricted" && action != "query" {
		return out, problem(403, "SECURITY_RESTRICTED_DELIVERY", "已复核高敏资源默认禁止下载、API与ETL外发；当前未开放例外通道")
	}
	for _, row := range rows {
		if row.Kind != "policy" {
			continue
		}
		d, err := decode[SecurityDefinition](row.Definition)
		if err != nil {
			return out, err
		}
		if !securityApplies(d, ref, out.Level, p, action) {
			continue
		}
		if d.Deny {
			return out, problem(403, "SECURITY_POLICY_DENIED", "当前资源、主体或交付方式被已发布安全策略限制")
		}
		out.Controls = append(out.Controls, GovernanceRef{ID: row.ID, Version: row.Version})
		out.MaskFields = append(out.MaskFields, d.MaskFields...)
		out.RequireProof = out.RequireProof || d.RequireProof
		out.Visible = out.Visible || d.Visible
		out.FileMark = out.FileMark || d.FileMark
		out.Invisible = out.Invisible || d.Invisible
		out.RowFilters = append(out.RowFilters, d.RowFilters...)
		if d.FileMark && action != "download" {
			return out, problem(403, "SECURITY_CONTENT_MARK_UNSUPPORTED", "此交付方式无法生成要求的文件内容标记")
		}
		if d.Template != nil {
			var active int64
			if err = tx.Model(&SecurityControl{}).Where("id=? AND kind='watermark' AND published_version>0", d.Template.ID).Count(&active).Error; err != nil {
				return out, err
			}
			if active == 0 {
				return out, problem(409, "SECURITY_TEMPLATE_UNAVAILABLE", "水印模板已停用")
			}

			var v SecurityControlVersion
			if err = tx.Where("control_id=? AND version=? AND state='published'", d.Template.ID, d.Template.Version).Take(&v).Error; err != nil {
				return out, problem(409, "SECURITY_TEMPLATE_UNAVAILABLE", "安全策略引用的水印模板已停用，请联系安全管理员")
			}
			template, err := decode[SecurityDefinition](v.Definition)
			if err != nil {
				return out, err
			}
			if out.Text != "" && out.Text != template.Text {
				return out, problem(409, "SECURITY_TEMPLATE_CONFLICT", "多个安全策略要求不同水印模板，请先核对冲突")
			}
			out.Text = template.Text
			out.Controls = append(out.Controls, *d.Template)
		}
	}
	// A published data service keeps the protections of its source result.
	if ref.Type == "api_resource" {
		_, managed, err := managedResource(tx, ref, false)
		if err != nil {
			return out, err
		}
		cfg, err := decode[APIConfig](managed.Config)
		if err != nil {
			return out, err
		}
		if cfg.DataService != nil {
			base, err := get(tx, "dataset", cfg.DataService.DatasetID, false)
			if err != nil {
				return out, err
			}
			sources := []Record{base}
			if cfg.DataService.Mode == "latest" {
				var latest Record
				if err = tx.Where("kind='dataset' AND parent_id=? AND department_id=? AND state='published'", base.ParentID, base.DepartmentID).Order("created_at DESC,id DESC").Take(&latest).Error; err != nil {
					return out, err
				}
				if latest.ID != base.ID {
					sources = append(sources, latest)
				}
			}
			for _, source := range sources {
				inherited, err := s.securityPolicyCheckPath(tx, p, Resource{Type: "governed_dataset", ID: source.ID, Version: source.Version}, action, path)
				if err != nil {
					return out, err
				}
				if out.Text != "" && inherited.Text != "" && out.Text != inherited.Text {
					return out, problem(409, "SECURITY_TEMPLATE_CONFLICT", "接口与来源成果的水印要求存在冲突")
				}
				if out.Text == "" {
					out.Text = inherited.Text
				}
				if slices.Index(securityLevels, inherited.Level) > slices.Index(securityLevels, out.Level) {
					out.Level = inherited.Level
				}
				out.MaskFields = append(out.MaskFields, inherited.MaskFields...)
				out.Controls = append(out.Controls, inherited.Controls...)
				out.RequireProof = out.RequireProof || inherited.RequireProof
				out.Visible = out.Visible || inherited.Visible
				out.FileMark = out.FileMark || inherited.FileMark
				out.Invisible = out.Invisible || inherited.Invisible
				out.RowFilters = append(out.RowFilters, inherited.RowFilters...)
			}
		}
	}
	if ref.Type == "governed_dataset" {
		lineage, sources, err := securityDatasetSources(tx, ref)
		if err != nil {
			return out, err
		}
		for _, source := range sources {
			inherited, err := s.securityPolicyCheckPath(tx, p, source, action, path)
			if err != nil {
				return out, err
			}
			if len(inherited.RowFilters) > 0 {
				return out, problem(403, "SECURITY_FILTER_LINEAGE_UNSUPPORTED", "来源行过滤不能在已生成成果上重新应用，请生成独立受控成果")
			}
			mapped := []string{}
			for field, origins := range lineage {
				if slices.ContainsFunc(origins, func(origin GovernanceFieldOrigin) bool {
					return origin.Resource == source && slices.Contains(inherited.MaskFields, origin.Field)
				}) {
					mapped = append(mapped, field)
				}
			}
			if len(inherited.MaskFields) > 0 && len(lineage) == 0 {
				_, fields, err := resource(tx, ref, false)
				if err != nil {
					return out, err
				}
				for _, field := range fields {
					mapped = append(mapped, field.Name)
				}
			}
			inherited.MaskFields = mapped
			if err = securityMergeDecision(&out, inherited); err != nil {
				return out, err
			}
		}
	}
	slices.SortFunc(out.Controls, func(a, b GovernanceRef) int {
		if a.ID != b.ID {
			return strings.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Version, b.Version)
	})
	out.Controls = slices.Compact(out.Controls)
	slices.Sort(out.MaskFields)
	out.MaskFields = slices.Compact(out.MaskFields)
	if action == "download" && out.Visible && !out.FileMark {
		return out, problem(403, "SECURITY_DOWNLOAD_MARK_REQUIRED", "文件下载需要选择文件内容标记；页面标记不能代替文件水印")
	}
	if action == "download" && (out.FileMark || out.Invisible) {
		if ref.Type != "file_resource" {
			return out, problem(403, "SECURITY_CONTENT_MARK_UNSUPPORTED", "此载体不支持文件内容标记")
		}
		_, managed, err := managedResource(tx, ref, false)
		if err != nil {
			return out, err
		}
		file, err := decode[FileConfig](managed.Config)
		if err != nil {
			return out, err
		}
		if !slices.Contains([]string{".xlsx", ".pdf"}, strings.ToLower(filepath.Ext(file.Name))) {
			return out, problem(409, "SECURITY_FILE_FORMAT_UNSUPPORTED", "此资源要求内容水印；当前仅支持Excel与PDF内容标记")
		}
	}
	if action == "download" && len(out.MaskFields) > 0 {
		return out, problem(403, "SECURITY_FILE_MASK_UNSUPPORTED", "完整文件无法按字段脱敏，请申请经过脱敏的独立文件资源")
	}
	if action == "etl" && (out.FileMark || out.Visible || out.Invisible) {
		return out, problem(403, "SECURITY_ETL_CONTENT_MARK_UNSUPPORTED", "当前ETL仅支持签名交付凭据，不支持嵌入内容水印，请调整策略或申请独立资源")
	}
	if len(out.RowFilters) > 0 {
		_, schema, err := resource(tx, ref, false)
		if err != nil {
			return out, err
		}
		if err = securityValidateRowFilters(out.RowFilters, schema); err != nil {
			return out, err
		}
		switch ref.Type {
		case "table_resource":
			if !slices.Contains([]string{"query", "etl"}, action) {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此交付方式不支持分页前行过滤")
			}
			_, managed, err := managedResource(tx, ref, false)
			if err != nil {
				return out, err
			}
			if managed.Adapter != "database" {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此适配器尚不支持分页前行过滤")
			}
			physical, err := decode[TableConfig](managed.Config)
			if err != nil {
				return out, err
			}
			source, err := get(tx, "source", physical.SourceID, false)
			if err != nil {
				return out, err
			}
			sourceDefinition, err := decode[SourceSecret](source.Body)
			if err != nil {
				return out, err
			}
			if !slices.Contains([]string{"postgres", "mysql", "oracle"}, sourceDefinition.Kind) {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此来源适配器不支持分页前行过滤")
			}
			if _, _, err = securityRowFilterSQL(sourceDefinition.Kind, out.RowFilters, schema, 0); err != nil {
				return out, err
			}
		case "governed_dataset":
			if _, _, err := securityRowFilterSQL("mysql", out.RowFilters, schema, 0); err != nil {
				return out, err
			}
			if !slices.Contains([]string{"query", "invoke"}, action) {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此交付方式不支持成果行过滤")
			}
			record, err := get(tx, "dataset", ref.ID, false)
			if err != nil {
				return out, err
			}
			data, err := decode[DatasetData](record.Body)
			if err != nil {
				return out, err
			}
			if data.GovernanceRunID == "" || data.Storage == nil {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此成果存储不支持分页前行过滤")
			}
		case "api_resource":
			if _, _, err := securityRowFilterSQL("mysql", out.RowFilters, schema, 0); err != nil {
				return out, err
			}
			if action != "invoke" {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此交付方式不支持接口行过滤")
			}
			_, managed, err := managedResource(tx, ref, false)
			if err != nil {
				return out, err
			}
			cfg, err := decode[APIConfig](managed.Config)
			if err != nil {
				return out, err
			}
			if cfg.DataService == nil || cfg.DataService.SourceSnapshot != nil {
				return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此接口适配器不支持分页前行过滤")
			}
		default:
			return out, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此载体尚不支持分页和加工前行过滤")
		}
	}
	return out, nil
}
func securityMaskRows(rows []map[string]interface{}, fields []string) {
	for _, row := range rows {
		for _, f := range fields {
			if v, ok := row[f]; ok && v != nil {
				row[f] = "***"
			}
		}
	}
}
func (s *Server) securityResourceContext(c *gin.Context) {
	version, e := intQuery(c, "contract_version", 1, 100000000)
	if e != nil {
		s.fail(c, e)
		return
	}
	ref := Resource{Type: c.Query("resource_type"), ID: c.Query("resource_id"), Version: version}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	var sharing *SharingSnapshot
	if !platform(p, "security") || !role(p, "admin", "auditor") {
		if snapshot, err := resolveActiveSharing(tx, ref, false); err != nil {
			e = err
			s.fail(c, e)
			return
		} else {
			sharing = &snapshot
		}
	} else {
		if _, _, e = resource(tx, ref, false); e != nil {
			s.fail(c, e)
			return
		}
	}
	d, e := s.securityPolicyCheck(tx, p, ref, string(resourceMethod(ref)))
	if sharing != nil {
		mount, err := get(tx, "mount_revision", sharing.MountRevisionID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		revision, err := decode[MountRevisionData](mount.Body)
		if err != nil {
			s.fail(c, err)
			return
		}
		visible := []string{}
		for _, field := range revision.Mapping {
			visible = append(visible, field)
		}
		d.MaskFields = slices.DeleteFunc(d.MaskFields, func(field string) bool { return !slices.Contains(visible, field) })
		d.Controls = []GovernanceRef{}
		// Predicate literals may concern a masked field; consumers only receive the filtered rows.
		d.RowFilters = []SecurityRowFilter{}
	}

	if e != nil {
		var v *Problem
		if errors.As(e, &v) {
			c.JSON(200, gin.H{"resource": ref, "blocked": true, "reason": v.Message, "level": d.Level, "mask_fields": d.MaskFields, "visible": d.Visible, "require_proof": d.RequireProof, "file_mark": d.FileMark, "invisible": d.Invisible, "row_filters": d.RowFilters, "controls": d.Controls})
			return
		}
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"resource": ref, "blocked": false, "level": d.Level, "mask_fields": d.MaskFields, "visible": d.Visible, "require_proof": d.RequireProof, "file_mark": d.FileMark, "invisible": d.Invisible, "row_filters": d.RowFilters, "controls": d.Controls})
}
func (s *Server) securityPreflight(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		Resource   Resource `json:"resource"`
		Action     string   `json:"action"`
		Department string   `json:"department"`
		Roles      []string `json:"roles"`
	}](b)
	if e != nil || !slices.Contains(securityActions, v.Action) || len(v.Department) > 128 || len(v.Roles) > 6 || slices.ContainsFunc(v.Roles, func(r string) bool {
		return !slices.Contains([]string{"admin", "auditor", "provider", "consumer", "operator", "governor"}, r)
	}) {
		return 0, nil, "", invalid
	}
	if _, _, e = resource(tx, v.Resource, false); e != nil {
		return 0, nil, "", e
	}
	subject := p
	if v.Department != "" {
		subject.DepartmentID = v.Department
	}
	if len(v.Roles) > 0 {
		subject.Roles = v.Roles
	}
	d, e := s.securityPolicyCheck(tx, subject, v.Resource, v.Action)
	if e != nil {
		var p *Problem
		if errors.As(e, &p) {
			return 200, encode(gin.H{"allowed": false, "decision": d, "reason": p.Message}), v.Resource.ID, nil
		}
		return 0, nil, "", e
	}
	return 200, encode(gin.H{"allowed": true, "decision": d, "reason": "仍须核对实际使用授权；本次预检不会产生授权"}), v.Resource.ID, nil
}

func securityResourceFrozen(tx *gorm.DB, ref Resource) error {
	var frozen int64
	if e := tx.Model(&SecurityIncident{}).Where("resource_id=? AND state='frozen'", ref.ID).Where("resource @> ?::jsonb", string(encode(ref))).Count(&frozen).Error; e != nil {
		return e
	}
	if frozen > 0 {
		return problem(403, "SECURITY_RESOURCE_FROZEN", "此资源版本已被安全冻结，请查看安全事件处置进度")
	}
	return nil
}

// Maintenance previews do not have a watermark viewer or a delivery grant.
// Refuse them when an approved control requires a signed or marked delivery.
func (s *Server) securityPreviewCheck(tx *gorm.DB, p identity.Principal, ref Resource, action string) (SecurityDecision, error) {
	d, e := s.securityPolicyCheck(tx, p, ref, action)
	if e != nil {
		return d, e
	}
	if d.RequireProof || d.Visible || d.FileMark || d.Invisible || len(d.RowFilters) > 0 {
		return d, problem(403, "SECURITY_CONTROLLED_PREVIEW_REQUIRED", "此资源须通过受控交付入口查看水印与凭据，维护预览暂不支持该保护要求")
	}
	return d, nil
}

// Field lineage maps protection through renames, joins, aggregates and scripts.
// Legacy results without field lineage conservatively protect every output field.
func securityDatasetSources(tx *gorm.DB, ref Resource) (map[string][]GovernanceFieldOrigin, []Resource, error) {
	r, e := get(tx, "dataset", ref.ID, false)
	if e != nil {
		return nil, nil, e
	}
	d, e := decode[DatasetData](r.Body)
	if e != nil {
		return nil, nil, e
	}
	sources := []Resource{}
	lineage := map[string][]GovernanceFieldOrigin{}
	add := func(resource Resource) {
		if resource.ID != "" && resource != ref && !slices.Contains(sources, resource) {
			sources = append(sources, resource)
		}
	}
	if d.GovernanceRunID != "" {
		var run GovernanceRun
		if e = tx.Where("id=?", d.GovernanceRunID).Take(&run).Error; e == nil {
			var report governanceFlowReport
			if e = json.Unmarshal(run.Result, &report); e != nil {
				return nil, nil, e
			}
			if report.Lineage != nil {
				lineage = report.Lineage
			}
			for _, origins := range lineage {
				for _, origin := range origins {
					add(origin.Resource)
				}
			}
			var execution governanceFlowExecution
			if e = json.Unmarshal(run.Definition, &execution); e != nil {
				return nil, nil, e
			}
			for _, input := range execution.Inputs {
				if input.UploadID == "" {
					add(input.Resource)
				}
			}
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return nil, nil, e
		}
	}
	if d.Governance != nil && d.Governance.Source != nil {
		add(*d.Governance.Source)
	} else if len(sources) == 0 && d.Definition.SourceObjectID != "" {
		source, err := get(tx, "object", d.Definition.SourceObjectID, false)
		if err == nil {
			add(Resource{Type: "raw_object", ID: source.ID, Version: source.Version})
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, err
		}
	}
	slices.SortFunc(sources, func(a, b Resource) int {
		if a.Type != b.Type {
			return strings.Compare(a.Type, b.Type)
		}
		if a.ID != b.ID {
			return strings.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Version, b.Version)
	})
	return lineage, sources, nil
}
func securityMergeDecision(out *SecurityDecision, inherited SecurityDecision) error {
	if out.Text != "" && inherited.Text != "" && out.Text != inherited.Text {
		return problem(409, "SECURITY_TEMPLATE_CONFLICT", "成果与来源的水印要求存在冲突")
	}
	if out.Text == "" {
		out.Text = inherited.Text
	}
	if slices.Index(securityLevels, inherited.Level) > slices.Index(securityLevels, out.Level) {
		out.Level = inherited.Level
	}
	out.MaskFields = append(out.MaskFields, inherited.MaskFields...)
	out.Controls = append(out.Controls, inherited.Controls...)
	out.RequireProof = out.RequireProof || inherited.RequireProof
	out.Visible = out.Visible || inherited.Visible
	out.FileMark = out.FileMark || inherited.FileMark
	out.Invisible = out.Invisible || inherited.Invisible
	out.RowFilters = append(out.RowFilters, inherited.RowFilters...)
	return nil
}
