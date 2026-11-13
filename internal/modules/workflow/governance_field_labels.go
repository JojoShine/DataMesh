package workflow

import (
	"encoding/json"
	"gorm.io/gorm"
)

// Only labels for already-authorized execution inputs are read; technical field
// identifiers and pinned processing contracts are never rewritten.
func governanceInputFieldLabels(tx *gorm.DB, inputs map[string]governanceInput) (map[string]map[string]string, error) {
	labels := map[string]map[string]string{}
	for _, input := range inputs {
		id := input.Resource.ID
		if input.UploadID != "" {
			labels[id] = input.FieldLabels
			continue
		}
		if _, ok := labels[id]; ok {
			continue
		}
		var row struct{ Items json.RawMessage }
		err := tx.Table("business_records r").Select("v.body->'definition'->'items' AS items").
			Joins("JOIN business_records d ON d.kind='directory' AND d.id=r.parent_id AND d.department_id=r.department_id").
			Joins("JOIN business_records v ON v.kind='directory_revision' AND v.id=d.body->>'published_id' AND v.parent_id=d.id AND v.department_id=d.department_id").Where("r.id=?", id).Scan(&row).Error
		if err != nil {
			return nil, err
		}
		labels[id] = map[string]string{}
		if len(row.Items) > 0 {
			items, err := decode[[]DirectoryItem](row.Items)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				labels[id][item.Name] = item.BusinessName
			}
		}
	}
	return labels, nil
}

func governanceOutputFieldLabels(graph GovernanceDataflow, plan governanceFlowPlan, sources map[string]map[string]string, model GovernanceModel) map[string]string {
	nodes := map[string]GovernanceFlowNode{}
	labels := map[string]map[string]string{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, id := range plan.Order {
		node := nodes[id]
		current := map[string]string{}
		input := map[string]string{}
		if len(node.Inputs) > 0 {
			input = labels[node.Inputs[0].Node]
		}
		for name, label := range input {
			current[name] = label
		}
		switch node.Kind {
		case "source":
			current = map[string]string{}
			for _, name := range node.Source.Fields {
				current[name] = sources[node.Source.Resource.ID][name]
			}
		case "project", "transform":
			current = map[string]string{}
			for _, column := range node.Columns {
				current[column.Target] = input[column.Source]
			}
		case "join":
			current = map[string]string{}
			for _, column := range node.Join.Columns {
				index := 0
				if column.Side == "right" {
					index = 1
				}
				current[column.Target] = labels[node.Inputs[index].Node][column.Source]
			}
		case "aggregate":
			current = map[string]string{}
			for _, name := range node.GroupBy {
				current[name] = input[name]
			}
			for _, aggregate := range node.Aggregates {
				if label := input[aggregate.Source]; label != "" {
					current[aggregate.Target] = map[string]string{"sum": "合计", "count": "数量", "min": "最小值", "max": "最大值", "avg": "平均值"}[aggregate.Operation] + " · " + label
				}
			}
		case "script":
			current = map[string]string{}
			for _, field := range node.Script.Fields {
				current[field.Name] = field.Description
				if field.BusinessName != "" {
					current[field.Name] = field.BusinessName
				}
			}
		case "output":
			for _, field := range model.Fields {
				if field.BusinessName != "" || field.Description != "" {
					current[field.Name] = field.Description
					if field.BusinessName != "" {
						current[field.Name] = field.BusinessName
					}
				}
			}
		}
		labels[id] = current
	}
	return labels[plan.OutputNode]
}
