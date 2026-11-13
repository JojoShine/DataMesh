package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

// Public treatment descriptions explain operations, never code or predicate values.
func governanceTreatmentSummary(graph GovernanceDataflow) []GovernanceTreatment {
	out := []GovernanceTreatment{}
	for _, node := range graph.Nodes {
		step := GovernanceTreatment{Kind: node.Kind}
		switch node.Kind {
		case "source", "output":
			continue
		case "project":
			step.Name, step.Description = "整理数据项", fmt.Sprintf("选择并整理 %d 个输出数据项。", len(node.Columns))
		case "transform":
			step.Name = "清理文字"
			rules := []string{}
			for _, rule := range []struct{ key, label string }{{"trim", "去除首尾空白"}, {"upper", "统一为大写"}, {"lower", "统一为小写"}} {
				count := 0
				for _, column := range node.Columns {
					if column.Rule == rule.key {
						count++
					}
				}
				if count > 0 {
					rules = append(rules, fmt.Sprintf("%d 项%s", count, rule.label))
				}
			}
			step.Description = "按配置保留原值和输出名称。"
			if len(rules) > 0 {
				step.Description = strings.Join(rules, "；") + "。"
			}
		case "filter":
			step.Name, step.Description = "筛选记录", "按业务配置的条件保留记录，其余记录不进入结果。"
		case "deduplicate":
			step.Name, step.Description = "去除重复记录", fmt.Sprintf("按 %d 项依据判断重复，保留来源顺序中的第一条。", len(node.Keys))
		case "quality":
			step.Name, step.Description = "检查数据质量", fmt.Sprintf("执行 %d 项质量检查，分别保留合格与不合格记录的处理证据。", len(node.Checks))
			if node.Template != nil {
				step.Description = "按固定版本质量模板检查，保留本次检查证据。"
			}
		case "code_map":
			step.Name, step.Description = "统一分类值", "按已配置的业务标准统一分类值，并按约定处理未匹配值。"
		case "join":
			step.Name, step.Description = "关联数据", "按约定的对应关系补充多份来源数据。"
		case "aggregate":
			step.Name, step.Description = "分组统计", fmt.Sprintf("按 %d 项分类计算 %d 项统计结果。", len(node.GroupBy), len(node.Aggregates))
		case "script":
			step.Name, step.Description = "自定义处理", "按本次运行固定的自定义规则处理数据。"
		default:
			continue
		}
		out = append(out, step)
	}
	return out
}

func governanceOutputDepartment(executionDepartment string, proof *GovernanceProvenance) string {
	if proof != nil && proof.SingleSource && proof.Source != nil && proof.ProviderDepartmentID != "" {
		return proof.ProviderDepartmentID
	}
	return executionDepartment
}

func governanceDatasetCanRead(tx *gorm.DB, p identity.Principal, record Record) (bool, error) {
	if department(p, record) {
		return true, nil
	}
	if record.OwnerID != p.UserID {
		return false, nil
	}
	var count int64
	err := tx.Table("governance_outputs o").Joins("JOIN governance_runs r ON r.id=o.run_id").Where("o.dataset_id=? AND r.actor_id=?", record.ID, p.UserID).Count(&count).Error
	return count == 1, err
}

func governanceOutputProvenance(ctx context.Context, tx *sql.Tx, def governanceFlowExecution) (*GovernanceProvenance, error) {
	var graph *GovernanceDataflow
	for _, pinned := range def.Pinned {
		if pinned.Kind == "dataflow" && pinned.Ref == def.Flow {
			graph = pinned.Definition.Dataflow
			break
		}
	}
	if graph == nil {
		return nil, invalid
	}
	materialized, err := governanceMaterializeFlow(*graph, def.Parameters, def.Inputs)
	if err != nil {
		return nil, err
	}
	graph = &materialized
	out := &GovernanceProvenance{Steps: governanceTreatmentSummary(*graph)}
	var source *GovernanceFlowNode
	for i := range graph.Nodes {
		if graph.Nodes[i].Kind != "source" {
			continue
		}
		if source != nil {
			return out, nil
		}
		source = &graph.Nodes[i]
	}
	if source == nil || source.Source == nil || len(def.Inputs) != 1 {
		return out, nil
	}
	input, ok := def.Inputs[source.ID]
	if !ok || input.Resource != source.Source.Resource {
		return nil, invalid
	}
	if input.MasterSnapshotID != "" {
		out.Steps = append([]GovernanceTreatment{{Kind: "master", Name: "主数据确认", Description: "按固定实体确认版本形成成果，字段血缘保留原始来源；各来源依次读取。"}}, out.Steps...)
		return out, nil
	}
	ref := input.Resource
	kind := ref.Type
	if kind == "raw_object" {
		kind = "object"
	}
	if kind == "governed_dataset" {
		kind = "dataset"
	}
	var inherited json.RawMessage
	if err := tx.QueryRowContext(ctx, `SELECT department_id,COALESCE(NULLIF(body->'definition'->>'name',''),NULLIF(body->>'name',''),NULLIF(body->>'qualified_name',''),id),COALESCE(body->'governance','null'::jsonb) FROM business_records WHERE kind=$1 AND id=$2 AND version=$3`, kind, ref.ID, ref.Version).Scan(&out.ProviderDepartmentID, &out.SourceName, &inherited); err != nil {
		return nil, err
	}
	out.SingleSource, out.Source = true, &ref
	if kind == "dataset" && string(inherited) != "null" {
		var parent GovernanceProvenance
		if err := json.Unmarshal(inherited, &parent); err != nil {
			return nil, err
		}
		if parent.SingleSource && parent.ProviderDepartmentID == out.ProviderDepartmentID {
			out.OriginalDirectoryID, out.OriginalDirectoryName = parent.OriginalDirectoryID, parent.OriginalDirectoryName
		}
	}
	var directoryID, directoryName string
	err = tx.QueryRowContext(ctx, `SELECT d.id,dr.body->'definition'->>'name' FROM business_records m JOIN business_records d ON d.kind='directory' AND d.id=m.parent_id JOIN business_records dr ON dr.kind='directory_revision' AND dr.id=d.body->>'published_id' WHERE m.kind='mount' AND m.state='active' AND m.body->>'resource_key'=$1 AND d.department_id=$2 AND d.state='published'`, resourceKey(ref), out.ProviderDepartmentID).Scan(&directoryID, &directoryName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		out.OriginalDirectoryID, out.OriginalDirectoryName = directoryID, directoryName
	}
	return out, nil
}
