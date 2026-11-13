package workflow

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GovernanceRef always pins an immutable definition version.
type GovernanceRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}
type GovernanceDefinition struct {
	Metric           *GovernanceMetric          `json:"metric,omitempty"`
	Master           *GovernanceMaster          `json:"master,omitempty"`
	Scenario         *GovernanceScenario        `json:"scenario,omitempty"`
	DomainScope      string                     `json:"domain_scope,omitempty"`
	EnterpriseDomain *GovernanceRef             `json:"enterprise_domain,omitempty"`
	QualityTemplate  *GovernanceQualityTemplate `json:"quality_template,omitempty"`
	Workflow         *GovernanceWorkflow        `json:"workflow,omitempty"`
	Dataflow         *GovernanceDataflow        `json:"dataflow,omitempty"`
	Metadata         *GovernanceMetadata        `json:"metadata,omitempty"`
	Name             string                     `json:"name"`
	Description      string                     `json:"description"`
	OwnerID          string                     `json:"owner_id"`
	StewardID        string                     `json:"steward_id"`
	Domain           *GovernanceRef             `json:"domain,omitempty"`
	Standard         *GovernanceStandard        `json:"standard,omitempty"`
	Model            *GovernanceModel           `json:"model,omitempty"`
}
type GovernanceStandard struct {
	Code          string            `json:"code"`
	LogicalType   string            `json:"logical_type"`
	Length        int               `json:"length"`
	Precision     int               `json:"precision"`
	Scale         int               `json:"scale"`
	Nullable      bool              `json:"nullable"`
	Sensitive     bool              `json:"sensitive"`
	Pattern       string            `json:"pattern"`
	Values        []string          `json:"values"`
	ValueMeanings map[string]string `json:"value_meanings,omitempty"`
}
type GovernanceField struct {
	BusinessName string         `json:"business_name,omitempty"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	LogicalType  string         `json:"logical_type"`
	Length       int            `json:"length"`
	Precision    int            `json:"precision"`
	Scale        int            `json:"scale"`
	Nullable     bool           `json:"nullable"`
	PrimaryKey   bool           `json:"primary_key"`
	Sensitive    bool           `json:"sensitive"`
	Standard     *GovernanceRef `json:"standard,omitempty"`
}
type GovernanceRelation struct {
	Name         string        `json:"name"`
	Target       GovernanceRef `json:"target"`
	Fields       []string      `json:"fields"`
	TargetFields []string      `json:"target_fields"`
	Cardinality  string        `json:"cardinality"`
}
type GovernanceContract struct {
	FreshnessSeconds  int     `json:"freshness_seconds"`
	MaxFailureRate    float64 `json:"max_failure_rate"`
	UsageRestrictions string  `json:"usage_restrictions"`
}
type GovernanceModel struct {
	DataLayer string               `json:"data_layer,omitempty"`
	Layer     string               `json:"layer"`
	Schema    string               `json:"schema"`
	Table     string               `json:"table"`
	Fields    []GovernanceField    `json:"fields"`
	Relations []GovernanceRelation `json:"relations"`
	Contract  GovernanceContract   `json:"contract"`
}
type GovernanceChange struct {
	Code     string `json:"code"`
	Field    string `json:"field,omitempty"`
	Breaking bool   `json:"breaking"`
}

var governanceIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
var governanceCode = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,127}$`)

func validGovernanceRef(r *GovernanceRef) bool { return r == nil || (valid(r.ID, 64) && r.Version > 0) }
func validGovernanceType(kind string, length, precision, scale int) bool {
	if length < 0 || length > 65535 || precision < 0 || scale < 0 {
		return false
	}
	switch kind {
	case "string":
		return precision == 0 && scale == 0
	case "decimal":
		return length == 0 && precision >= 1 && precision <= 38 && scale <= precision
	case "integer", "boolean", "date", "datetime":
		return length == 0 && precision == 0 && scale == 0
	}
	return false
}
func validateGovernanceDefinition(kind string, v GovernanceDefinition) error {
	if e := validateCapabilityDefinition(kind, v); e != nil {
		return e
	}
	if kind != "domain" && (v.DomainScope != "" || v.EnterpriseDomain != nil) {
		return invalid
	}
	if kind == "domain" && (!slices.Contains([]string{"", "department", "enterprise"}, v.DomainScope) || !validGovernanceRef(v.EnterpriseDomain) || (v.DomainScope == "enterprise" && v.EnterpriseDomain != nil)) {
		return invalid
	}
	if kind == "domain" && (v.DomainScope == "enterprise" || v.EnterpriseDomain != nil) && !valid(v.Description, 4000) {
		return problem(400, "GOVERNANCE_DOMAIN_DESCRIPTION", "请说明统一业务边界或本部门职责范围")
	}

	if kind != "quality_template" && v.QualityTemplate != nil {
		return invalid
	}
	if !valid(v.Name, 200) || len(v.Description) > 4000 || !valid(v.OwnerID, 64) || !valid(v.StewardID, 64) || !validGovernanceRef(v.Domain) {
		return invalid
	}
	if kind != "workflow" && v.Workflow != nil {
		return invalid
	}
	if kind != "dataflow" && v.Dataflow != nil {
		return invalid
	}
	if kind != "metadata" && v.Metadata != nil {
		return invalid
	}
	switch kind {
	case "master", "scenario":
	case "quality_template":
		if v.QualityTemplate == nil || v.Model != nil || v.Workflow != nil || v.Dataflow != nil || v.Standard != nil || v.Metadata != nil {
			return invalid
		}
		if err := validateGovernanceQualityTemplate(*v.QualityTemplate); err != nil {
			return err
		}
	case "workflow":
		if v.Workflow == nil || v.Dataflow != nil || v.Model != nil || v.Standard != nil || v.Metadata != nil {
			return invalid
		}
		if err := validateGovernanceWorkflow(*v.Workflow); err != nil {
			return err
		}
	case "dataflow":
		if v.Dataflow == nil || v.Model != nil || v.Standard != nil || v.Metadata != nil || len(v.Dataflow.Nodes) < 2 || len(v.Dataflow.Nodes) > 40 {
			return invalid
		}
		for _, node := range v.Dataflow.Nodes {
			if node.Script != nil {
				if err := validateGovernanceScript(node.Script); err != nil {
					return err
				}
			}
		}
		if _, err := governanceFlowParameters(*v.Dataflow); err != nil {
			return err
		}
	case "domain":
		if v.Domain != nil || v.Model != nil || v.Standard != nil {
			return invalid
		}
	case "term":
		if v.Model != nil || v.Standard != nil || !valid(v.Description, 4000) {
			return invalid
		}
	case "standard":
		if v.Model != nil || v.Standard == nil {
			return invalid
		}
		s := v.Standard
		if !governanceCode.MatchString(s.Code) || !validGovernanceType(s.LogicalType, s.Length, s.Precision, s.Scale) || len(s.Pattern) > 512 || len(s.Values) > 1000 {
			return invalid
		}
		var pattern *regexp.Regexp
		if s.Pattern != "" {
			var err error
			pattern, err = regexp.Compile(s.Pattern)
			if err != nil || s.LogicalType != "string" {
				return invalid
			}
		}
		if s.ValueMeanings != nil {
			if len(s.ValueMeanings) != len(s.Values) {
				return problem(400, "GOVERNANCE_CODE_MEANING_REQUIRED", "每个码值必须有且仅有一条业务含义")
			}
			for _, value := range s.Values {
				if !valid(s.ValueMeanings[value], 1000) {
					return problem(400, "GOVERNANCE_CODE_MEANING_REQUIRED", "请为每个码值填写业务含义")
				}
			}
		}
		seen := map[string]bool{}
		for _, value := range s.Values {
			if len(value) > 1000 || seen[value] || (s.Length > 0 && len([]rune(value)) > s.Length) || (pattern != nil && !pattern.MatchString(value)) {
				return invalid
			}
			seen[value] = true
			switch s.LogicalType {
			case "integer":
				if _, err := strconv.ParseInt(value, 10, 64); err != nil {
					return invalid
				}
			case "boolean":
				if value != "true" && value != "false" {
					return invalid
				}
			case "decimal":
				if _, err := parseImportValue(value, DirectoryItem{LogicalType: "decimal", Precision: &s.Precision, Scale: &s.Scale}); err != nil {
					return invalid
				}
				whole := strings.Split(strings.TrimLeft(value, "+-"), ".")[0]
				if len(strings.TrimLeft(whole, "0")) > s.Precision-s.Scale {
					return invalid
				}
			case "date":
				if _, err := time.Parse("2006-01-02", value); err != nil {
					return invalid
				}
			case "datetime":
				if _, err := parseImportValue(value, DirectoryItem{LogicalType: "datetime"}); err != nil {
					return invalid
				}
			}
		}
	case "metadata":
		if v.Standard != nil || v.Model != nil || v.Metadata == nil || !valid(v.Metadata.ObjectID, 128) || !valid(v.Metadata.ObservationID, 64) || len(v.Metadata.Fields) > 2000 {
			return invalid
		}
		seen := map[string]bool{}
		for _, f := range v.Metadata.Fields {
			if !valid(f.Name, 128) || seen[f.Name] || len(f.Description) > 1000 || !validGovernanceRef(f.Standard) || !validGovernanceRef(f.Term) {
				return invalid
			}
			seen[f.Name] = true
		}
	case "model":
		if v.Standard != nil || v.Model == nil {
			return invalid
		}
		m := v.Model
		if !validDataLayer(m.DataLayer) || !slices.Contains([]string{"conceptual", "logical", "physical"}, m.Layer) || len(m.Fields) > 200 || len(m.Relations) > 50 || m.Contract.FreshnessSeconds < 0 || m.Contract.FreshnessSeconds > 31536000 || m.Contract.MaxFailureRate < 0 || m.Contract.MaxFailureRate > 1 || len(m.Contract.UsageRestrictions) > 4000 {
			return invalid
		}
		if m.Layer != "conceptual" && len(m.Fields) == 0 {
			return invalid
		}
		if m.Layer == "physical" && (!governanceIdentifier.MatchString(m.Schema) || !governanceIdentifier.MatchString(m.Table)) {
			return invalid
		}
		fields := map[string]GovernanceField{}
		keys := 0
		for _, f := range m.Fields {
			if !governanceIdentifier.MatchString(f.Name) || len(f.BusinessName) > 200 || len(f.Description) > 1000 || !validGovernanceType(f.LogicalType, f.Length, f.Precision, f.Scale) || !validGovernanceRef(f.Standard) {
				return invalid
			}
			if _, ok := fields[f.Name]; ok {
				return invalid
			}
			fields[f.Name] = f
			if f.PrimaryKey {
				keys++
				if f.Nullable {
					return invalid
				}
			}
		}
		if m.Layer == "physical" && keys == 0 {
			return problem(400, "MODEL_PRIMARY_KEY_REQUIRED", "物理模型必须定义非空主键，可选择多个字段作为复合主键")
		}
		relations := map[string]bool{}
		for _, r := range m.Relations {
			if !governanceIdentifier.MatchString(r.Name) || relations[r.Name] || !validGovernanceRef(&r.Target) || len(r.Fields) == 0 || len(r.Fields) != len(r.TargetFields) || !slices.Contains([]string{"one_to_one", "many_to_one", "one_to_many"}, r.Cardinality) {
				return invalid
			}
			relations[r.Name] = true
			seen := map[string]bool{}
			targetSeen := map[string]bool{}
			for i, f := range r.Fields {
				if _, ok := fields[f]; !ok || seen[f] || targetSeen[r.TargetFields[i]] || !governanceIdentifier.MatchString(r.TargetFields[i]) {
					return invalid
				}
				seen[f] = true
				targetSeen[r.TargetFields[i]] = true
			}
		}
	default:
		return invalid
	}
	return nil
}

func governanceChanges(kind string, a, b GovernanceDefinition) []GovernanceChange {
	out := []GovernanceChange{}
	if !reflect.DeepEqual(a.Master, b.Master) {
		out = append(out, GovernanceChange{"master_changed", "", a.Master != nil})
	}
	if !reflect.DeepEqual(a.Scenario, b.Scenario) {
		out = append(out, GovernanceChange{"scenario_changed", "", a.Scenario != nil})
	}
	if !reflect.DeepEqual(a.Metric, b.Metric) {
		out = append(out, GovernanceChange{"metric_changed", "", a.Metric != nil})
	}
	if kind == "quality_template" && !reflect.DeepEqual(a.QualityTemplate, b.QualityTemplate) {
		out = append(out, GovernanceChange{"quality_template_changed", "", true})
	}
	if kind == "workflow" && !reflect.DeepEqual(a.Workflow, b.Workflow) {
		out = append(out, GovernanceChange{"workflow_changed", "", true})
	}
	if kind == "dataflow" && !reflect.DeepEqual(a.Dataflow, b.Dataflow) {
		out = append(out, GovernanceChange{"dataflow_changed", "", true})
	}
	if kind == "model" && a.Model != nil && b.Model != nil {
		before := map[string]GovernanceField{}
		after := map[string]GovernanceField{}
		for _, f := range a.Model.Fields {
			before[f.Name] = f
		}
		for _, f := range b.Model.Fields {
			after[f.Name] = f
		}
		for _, f := range a.Model.Fields {
			n, ok := after[f.Name]
			if !ok {
				out = append(out, GovernanceChange{"field_removed", f.Name, true})
				continue
			}
			if f.LogicalType != n.LogicalType || f.Precision != n.Precision || f.Scale != n.Scale {
				out = append(out, GovernanceChange{"field_type_changed", f.Name, true})
			}
			if f.Length != n.Length {
				out = append(out, GovernanceChange{"field_length_changed", f.Name, (f.Length == 0 && n.Length > 0) || (n.Length > 0 && n.Length < f.Length)})
			}
			if f.Nullable != n.Nullable {
				out = append(out, GovernanceChange{"field_nullability_changed", f.Name, !n.Nullable})
			}
			if f.PrimaryKey != n.PrimaryKey {
				out = append(out, GovernanceChange{"primary_key_changed", f.Name, true})
			}
			if f.Sensitive != n.Sensitive {
				out = append(out, GovernanceChange{"field_sensitivity_changed", f.Name, !n.Sensitive})
			}
			if !reflect.DeepEqual(f.Standard, n.Standard) {
				out = append(out, GovernanceChange{"field_standard_changed", f.Name, true})
			}
		}
		for _, f := range b.Model.Fields {
			if _, ok := before[f.Name]; !ok {
				out = append(out, GovernanceChange{"field_added", f.Name, !f.Nullable || f.PrimaryKey})
			}
		}
		if a.Model.Schema != b.Model.Schema || a.Model.Table != b.Model.Table || a.Model.Layer != b.Model.Layer || a.Model.DataLayer != b.Model.DataLayer {
			out = append(out, GovernanceChange{"model_target_changed", "", true})
		}
		if !reflect.DeepEqual(a.Model.Relations, b.Model.Relations) {
			out = append(out, GovernanceChange{"relations_changed", "", true})
		}
		if !reflect.DeepEqual(a.Model.Contract, b.Model.Contract) {
			out = append(out, GovernanceChange{"contract_changed", "", true})
		}
	}
	if kind == "metadata" && !reflect.DeepEqual(a.Metadata, b.Metadata) {
		out = append(out, GovernanceChange{"metadata_changed", "", false})
	}
	if kind == "standard" && a.Standard != nil && b.Standard != nil && !reflect.DeepEqual(a.Standard, b.Standard) {
		before, after := *a.Standard, *b.Standard
		before.ValueMeanings, after.ValueMeanings = nil, nil
		if !reflect.DeepEqual(before, after) {
			out = append(out, GovernanceChange{"standard_changed", "", true})
		}
		if !reflect.DeepEqual(a.Standard.ValueMeanings, b.Standard.ValueMeanings) {
			breaking := false
			for value, meaning := range a.Standard.ValueMeanings {
				if meaning != b.Standard.ValueMeanings[value] {
					breaking = true
				}
			}
			out = append(out, GovernanceChange{"code_meanings_changed", "", breaking})
		}
	}
	if !reflect.DeepEqual(a.EnterpriseDomain, b.EnterpriseDomain) {
		out = append(out, GovernanceChange{"enterprise_domain_changed", "", true})
	}
	if !reflect.DeepEqual(a.Domain, b.Domain) {
		out = append(out, GovernanceChange{"domain_changed", "", false})
	}
	return out
}
func governanceDDL(v GovernanceDefinition, dialect string) (string, error) {
	if err := validateGovernanceDefinition("model", v); err != nil {
		return "", err
	}
	if v.Model.Layer != "physical" {
		return "", problem(400, "PHYSICAL_MODEL_REQUIRED", "仅物理模型可以预览建表语句")
	}
	if dialect != "postgres" && dialect != "mysql" {
		return "", invalid
	}
	quote := func(v string) string { return `"` + v + `"` }
	if dialect == "mysql" {
		quote = func(v string) string { return "`" + v + "`" }
	}
	columns := []string{}
	keys := []string{}
	for _, f := range v.Model.Fields {
		typ := ""
		switch f.LogicalType {
		case "integer":
			typ = "BIGINT"
		case "string":
			typ = "TEXT"
			if f.Length > 0 {
				typ = fmt.Sprintf("VARCHAR(%d)", f.Length)
			}
			if dialect == "mysql" && f.PrimaryKey && f.Length == 0 {
				return "", problem(400, "MYSQL_KEY_LENGTH_REQUIRED", "MySQL 字符串主键需指定长度")
			}
		case "decimal":
			typ = fmt.Sprintf("DECIMAL(%d,%d)", f.Precision, f.Scale)
		case "boolean":
			typ = "BOOLEAN"
		case "date":
			typ = "DATE"
		case "datetime":
			typ = "TIMESTAMP"
			if dialect == "mysql" {
				typ = "DATETIME(6)"
			}
		}
		column := "  " + quote(f.Name) + " " + typ
		if !f.Nullable {
			column += " NOT NULL"
		}
		columns = append(columns, column)
		if f.PrimaryKey {
			keys = append(keys, quote(f.Name))
		}
	}
	columns = append(columns, "  PRIMARY KEY ("+strings.Join(keys, ", ")+")")
	suffix := ";"
	if dialect == "mysql" {
		suffix = " ENGINE=InnoDB;"
	}
	return "CREATE TABLE " + quote(v.Model.Schema) + "." + quote(v.Model.Table) + " (\n" + strings.Join(columns, ",\n") + "\n)" + suffix, nil
}
