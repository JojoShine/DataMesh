package workflow

import (
	"slices"
	"time"

	"github.com/gin-gonic/gin"
)

type GovernanceQualityTemplate struct {
	Fields          []Field `json:"fields"`
	ReferenceFields []Field `json:"reference_fields"`
	Checks          []Check `json:"checks"`
}

func validateGovernanceCrossCheck(check Check, left []governanceFlowField, inputs [][]governanceFlowField) error {
	a, ok := flowFieldsByName(left)[check.Field]
	if !ok {
		return invalid
	}
	right := left
	if check.Kind == "reference_exists" {
		if len(inputs) != 2 || len(check.Parameters) != 1 {
			return invalid
		}
		right = inputs[1]
	} else if len(check.Parameters) != 2 {
		return invalid
	}
	b, ok := flowFieldsByName(right)[flowLiteralText(check.Parameters["other_field"])]
	if !ok {
		return invalid
	}
	numeric := func(value string) bool { return value == "integer" || value == "decimal" }
	if a.Field.LogicalType != b.Field.LogicalType && !(numeric(a.Field.LogicalType) && numeric(b.Field.LogicalType)) {
		return problem(400, "GOVERNANCE_CHECK_TYPE", "比较字段类型不兼容")
	}
	if check.Kind == "compare_fields" {
		op := flowLiteralText(check.Parameters["operator"])
		if !slices.Contains([]string{"eq", "ne", "gt", "gte", "lt", "lte"}, op) || (a.Field.LogicalType == "boolean" && op != "eq" && op != "ne") {
			return invalid
		}
	}
	return nil
}

func validateGovernanceQualityTemplate(value GovernanceQualityTemplate) error {
	if len(value.Fields) < 1 || len(value.Fields) > 100 || len(value.ReferenceFields) > 100 {
		return invalid
	}
	inputs := [][]governanceFlowField{{}}
	for _, fields := range [][]Field{value.Fields, value.ReferenceFields} {
		seen := map[string]bool{}
		for _, field := range fields {
			if !governanceIdentifier.MatchString(field.Name) || seen[field.Name] || field.LogicalType == "unsupported" {
				return invalid
			}
			precision, scale := 0, 0
			if field.Precision != nil {
				precision = *field.Precision
			}
			if field.Scale != nil {
				scale = *field.Scale
			}
			if !validGovernanceType(field.LogicalType, 0, precision, scale) {
				return invalid
			}
			seen[field.Name] = true
		}
	}
	for _, field := range value.Fields {
		inputs[0] = append(inputs[0], governanceFlowField{Field: field})
	}
	if len(value.ReferenceFields) > 0 {
		inputs = append(inputs, []governanceFlowField{})
		for _, field := range value.ReferenceFields {
			inputs[1] = append(inputs[1], governanceFlowField{Field: field})
		}
	}
	_, e := compileGovernanceFlowNode(GovernanceFlowNode{ID: "template", Kind: "quality", Checks: value.Checks}, inputs, nil, nil, nil)
	return e
}

func governanceTemplateGraph(graph GovernanceDataflow, templates map[GovernanceRef]GovernanceQualityTemplate) (GovernanceDataflow, error) {
	graph.Nodes = slices.Clone(graph.Nodes)
	for i, node := range graph.Nodes {
		if node.Template == nil {
			continue
		}
		template, ok := templates[*node.Template]
		if !ok || len(node.Checks) > 0 {
			return graph, problem(400, "GOVERNANCE_TEMPLATE_INVALID", "请选择已发布模板，模板模式不能同时填写内联规则")
		}
		graph.Nodes[i].Checks = slices.Clone(template.Checks)
	}
	return graph, nil
}

func governanceTemplatePlan(graph GovernanceDataflow, templates map[GovernanceRef]GovernanceQualityTemplate, plan governanceFlowPlan) error {
	for _, node := range graph.Nodes {
		if node.Template == nil {
			continue
		}
		template := templates[*node.Template]
		for side, expected := range [][]Field{template.Fields, template.ReferenceFields} {
			if len(expected) == 0 {
				continue
			}
			if len(node.Inputs) <= side {
				return invalid
			}
			actual := flowFieldsByName(plan.Outputs[node.Inputs[side]])
			for _, field := range expected {
				other, ok := actual[field.Name]
				if !ok || field.LogicalType != other.Field.LogicalType {
					return problem(400, "GOVERNANCE_TEMPLATE_SCHEMA", "实际输入与模板字段契约不一致")
				}
			}
		}
	}
	return nil
}

func (s *Server) governanceQualityTrend(c *gin.Context) {
	days, e := intQuery(c, "days", 30, 366)
	if e != nil {
		s.fail(c, e)
		return
	}
	zone := c.DefaultQuery("timezone", "UTC")
	if zone == "Local" {
		s.fail(c, invalid)
		return
	}
	if _, e = time.LoadLocation(zone); e != nil {
		s.fail(c, invalid)
		return
	}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	query := tx.Table("governance_run_events e").Joins("JOIN governance_runs r ON r.id=e.run_id").Where("r.workspace_id IS NOT NULL AND e.code='QUALITY_CHECKED' AND e.created_at>=?", time.Now().AddDate(0, 0, -days))
	query = workspaceQuery(query, "r.workspace_id")
	if !admin(p) {
		query = query.Where("r.department_id=? AND r.actor_id=?", p.DepartmentID, p.UserID)
	}
	if flow := c.Query("definition_id"); flow != "" {
		if !valid(flow, 128) {
			s.fail(c, invalid)
			return
		}
		query = query.Where("r.definition->'flow'->>'id'=?", flow)
	}
	rows := []struct {
		Day      string `json:"day"`
		Checked  int64  `json:"checked"`
		Passed   int64  `json:"passed"`
		Rejected int64  `json:"rejected"`
		Runs     int64  `json:"runs"`
	}{}
	if e = query.Select("to_char(e.created_at AT TIME ZONE ?,'YYYY-MM-DD') AS day,sum(e.input_count) AS checked,sum(e.output_count) AS passed,sum(e.input_count-e.output_count) AS rejected,count(DISTINCT r.id) AS runs", zone).Group("day").Order("day").Scan(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": rows, "days": days, "timezone": zone, "unit": "quality_node_row_checks", "observed_at": time.Now().UTC()})
}
