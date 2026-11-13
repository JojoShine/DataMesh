package workflow

import (
	"context"
	"fmt"
	"strings"
)

// Aggregate per-key counts and selected byte sizes before a join can expand its intermediate relation.
func (s *Server) governanceDorisJoinBudget(ctx context.Context, tx *governanceDorisStage, node GovernanceFlowNode, plan governanceFlowPlan, usedBytes int64) error {
	q := &governanceDorisSQL{}
	join := node.Join
	group := func(alias, side string, edge GovernanceFlowEdge, keys []string) string {
		fields := flowFieldsByName(plan.Outputs[edge])
		expressions := []string{}
		selects := []string{}
		for i, key := range keys {
			expression := q.value(alias, fields[key].Field)
			expressions = append(expressions, expression)
			selects = append(selects, expression+" AS k"+fmt.Sprint(i))
		}
		valueBytes := []string{}
		for _, column := range join.Columns {
			if column.Side == side {
				valueBytes = append(valueBytes, "COALESCE(LENGTH(CAST("+q.raw(alias, column.Source)+" AS STRING)),4)")
			}
		}
		bytes := "0"
		if len(valueBytes) > 0 {
			bytes = strings.Join(valueBytes, "+")
		}
		return "(SELECT " + strings.Join(selects, ",") + ",count(*) AS n,COALESCE(sum(" + bytes + "),0) AS bytes FROM governance_flow_stage " + alias + " WHERE " + q.input(alias, edge) + " GROUP BY " + strings.Join(expressions, ",") + ")"
	}
	left := group("l", "left", node.Inputs[0], join.LeftKeys)
	right := group("r", "right", node.Inputs[1], join.RightKeys)
	terms := []string{}
	for i := range join.LeftKeys {
		terms = append(terms, fmt.Sprintf("lg.k%d=rg.k%d", i, i))
	}
	joinSQL := "JOIN"
	rightCount := "rg.n"
	if join.Kind == "left" {
		joinSQL = "LEFT JOIN"
		rightCount = "COALESCE(rg.n,1)"
	}
	var rows, bytes int64
	query := "SELECT COALESCE(sum(lg.n*" + rightCount + "),0),COALESCE(sum(lg.bytes*" + rightCount + "+COALESCE(rg.bytes,0)*lg.n),0) FROM " + left + " lg " + joinSQL + " " + right + " rg ON " + strings.Join(terms, " AND ")
	if e := tx.QueryRowContext(ctx, query, q.args...).Scan(&rows, &bytes); e != nil {
		return e
	}
	overhead := int64(2)
	for _, column := range join.Columns {
		overhead += int64(len(column.Target) + 10)
	}
	// Both operands are bounded by the existing per-run limits before reaching this step.
	if rows > int64(s.etl.MaxRows) || usedBytes+bytes+rows*overhead > s.etl.MaxBytes {
		return problem(409, "GOVERNANCE_JOIN_LIMIT", "关联扩张可能超过治理行数或字节预算，请先筛选、投影或预聚合")
	}
	return nil
}
