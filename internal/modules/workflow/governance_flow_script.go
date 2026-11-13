package workflow

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"datamesh/internal/platform/coderunner"
)

type GovernanceFlowScript struct {
	Language       string                     `json:"language"`
	RuntimeVersion string                     `json:"runtime_version"`
	Source         string                     `json:"source"`
	Fields         []GovernanceField          `json:"fields"`
	Parameters     map[string]json.RawMessage `json:"parameters,omitempty"`
}

var scriptRuntimeVersion = regexp.MustCompile(`^(python|javascript)@sha256:[a-f0-9]{64}$`)

func validateGovernanceScript(script *GovernanceFlowScript) error {
	if script == nil || !slices.Contains([]string{"python", "javascript"}, script.Language) || !strings.HasPrefix(script.RuntimeVersion, script.Language+"@") || !scriptRuntimeVersion.MatchString(script.RuntimeVersion) || len(script.Source) == 0 || len(script.Source) > coderunner.MaxCodeBytes || len(script.Fields) == 0 || len(script.Fields) > 100 {
		return flowProblem("SCRIPT_CONTRACT_INVALID", "请填写语言、固定运行环境、代码及1–100个输出字段")
	}
	names := map[string]bool{}
	for _, field := range script.Fields {
		if names[field.Name] || !governanceIdentifier.MatchString(field.Name) || len(field.BusinessName) > 200 || len(field.Description) > 1000 || !validGovernanceType(field.LogicalType, field.Length, field.Precision, field.Scale) || field.Standard != nil || field.PrimaryKey && field.Nullable {
			return flowProblem("SCRIPT_CONTRACT_INVALID", "代码输出字段的名称、类型、精度或可空性不正确；标准约束请在结果模型中关联")
		}
		names[field.Name] = true
	}
	return nil
}
func scriptOutputFields(node GovernanceFlowNode, input []governanceFlowField) ([]governanceFlowField, error) {
	if e := validateGovernanceScript(node.Script); e != nil {
		return nil, e
	}
	origins := []GovernanceFieldOrigin{}
	sensitive := false
	for _, field := range input {
		sensitive = sensitive || field.Field.Sensitive
		for _, origin := range field.Origins {
			if !slices.Contains(origins, origin) {
				origins = append(origins, origin)
			}
		}
	}
	result := []governanceFlowField{}
	for _, declared := range node.Script.Fields {
		field := Field{Name: declared.Name, LogicalType: declared.LogicalType, Nullable: declared.Nullable, Sensitive: declared.Sensitive || sensitive}
		if declared.LogicalType == "decimal" {
			precision, scale := declared.Precision, declared.Scale
			field.Precision = &precision
			field.Scale = &scale
		}
		result = append(result, governanceFlowField{Field: field, Origins: slices.Clone(origins)})
	}
	return result, nil
}

type scriptExecutionError struct {
	*Problem
	NodeID       string
	Line, Column int
	Field        string
}

func (e *scriptExecutionError) Unwrap() error { return e.Problem }
func scriptFailure(node, code string, line, column int, field string) *scriptExecutionError {
	message := "代码节点 " + node + " 执行失败，请检查代码或返回字段"
	if line > 0 {
		message = "代码节点 " + node + " 第" + strconv.Itoa(line) + "行执行失败，请检查该行代码"
	}
	if field != "" {
		message = "代码节点 " + node + " 返回字段 " + field + " 不符合已声明的输出要求"
	}
	return &scriptExecutionError{Problem: &Problem{Status: 409, Code: code, Message: message}, NodeID: node, Line: line, Column: column, Field: field}
}
func normalizeScriptRow(node GovernanceFlowNode, row DataRow) (DataRow, error) {
	if len(row) != len(node.Script.Fields) {
		return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, "")
	}
	for _, field := range node.Script.Fields {
		raw, exists := row[field.Name]
		if !exists {
			return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, field.Name)
		}
		if bytes.Equal(raw, []byte("null")) {
			if !field.Nullable {
				return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, field.Name)
			}
			continue
		}
		invalidValue := false
		switch field.LogicalType {
		case "string", "date", "datetime", "decimal":
			var value string
			invalidValue = json.Unmarshal(raw, &value) != nil
		case "boolean":
			invalidValue = !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false"))
		case "integer":
			text := flowLiteralText(raw)
			number, ok := new(big.Int).SetString(text, 10)
			invalidValue = !ok
			if ok && node.Script.Language == "javascript" && len(raw) > 0 && raw[0] != '"' {
				limit := big.NewInt(9007199254740991)
				absolute := new(big.Int).Abs(number)
				invalidValue = absolute.Cmp(limit) > 0
			}
		default:
			invalidValue = true
		}
		if invalidValue {
			return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, field.Name)
		}
		model := GovernanceModel{Fields: []GovernanceField{field}}
		if e := validateGovernanceOutputRow(row, model, map[GovernanceRef]GovernanceStandard{}); e != nil {
			return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, field.Name)
		}
	}
	return row, nil
}
