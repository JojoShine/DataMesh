package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type streamResultKey struct {
	Node string `json:"node_id"`
	Port string `json:"port"`
	Key  string `json:"row_key"`
}
type streamResultValue struct {
	streamResultKey
	Payload    string `json:"payload"`
	Violations string `json:"violations"`
}

func (s *Server) streamMaterialize(ctx context.Context, row *GovernanceStream, def governanceFlowExecution, stopped bool) (bool, error) {
	if !dorisObjectID.MatchString(row.ID) {
		return false, invalid
	}
	table := streamResultTable(def, row.ID)
	db, e := dorisOpen(ctx, true)
	if e != nil {
		return false, e
	}
	defer db.Close()
	// The progress marker is committed in the same Doris insert as all changes.
	var offset int64
	if e = db.QueryRowContext(ctx, "SELECT COALESCE(MAX(source_offset),0) FROM "+table+" WHERE node_id='_dm_progress'").Scan(&offset); e != nil {
		return false, e
	}
	messages, next, end, e := streamCommittedMessages(ctx, streamOutputTopic(row.ID), offset)
	if e != nil {
		return false, e
	}
	graph, plan, _, e := governanceExecutionPlan(def)
	if e != nil {
		return false, e
	}
	allowed := map[string]bool{}
	for _, n := range graph.Nodes {
		if n.Kind == "quality" {
			allowed[n.ID] = true
		}
	}
	args := []interface{}{}
	marks := []string{}
	qualityArgs := []interface{}{}
	qualityMarks := []string{}
	drained := next >= end
	for _, msg := range messages {
		key, err := decode[streamResultKey](msg.Key)
		if err != nil {
			return false, err
		}
		if !((key.Node == plan.OutputNode && key.Port == "") || (allowed[key.Node] && (key.Port == "bad" || key.Port == "good"))) || len(key.Key) < 1 || len(key.Key) > 128 {
			return false, problem(409, "STREAM_RESULT_INVALID", "结果事件与固定处理方案不符")
		}
		value := streamResultValue{Payload: "{}", Violations: "[]"}
		deleted := len(msg.Value) == 0
		if !deleted {
			value, err = decode[streamResultValue](msg.Value)
			if err != nil {
				return false, err
			}
			if value.streamResultKey != key || !json.Valid([]byte(value.Payload)) || !json.Valid([]byte(value.Violations)) {
				return false, problem(409, "STREAM_RESULT_INVALID", "结果事件格式无效")
			}
		}
		if key.Port != "" {
			qualityArgs = append(qualityArgs, key.Node, key.Port, key.Key, msg.Offset, deleted, value.Payload, value.Violations)
			qualityMarks = append(qualityMarks, "(?,?,?,?,?,?,?)")
		} else {
			args = append(args, key.Node, key.Port, key.Key, msg.Offset, deleted, value.Payload, value.Violations)
			marks = append(marks, "(?,?,?,?,?,?,?)")
		}
	}
	// Quality evidence is idempotent by Kafka offset. Advance the durable cursor
	// only after both zones have accepted this batch, so interrupted writes replay.
	if len(qualityMarks) > 0 {
		if _, e = db.ExecContext(ctx, "INSERT INTO "+dorisDatabase("quality")+".live_"+row.ID+" VALUES "+strings.Join(qualityMarks, ","), qualityArgs...); e != nil {
			return false, e
		}
	}
	if next > offset {
		args = append(args, "_dm_progress", "", "cursor", next, true, "{}", "[]")
		marks = append(marks, "(?,?,?,?,?,?,?)")
		if _, e = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES "+strings.Join(marks, ","), args...); e != nil {
			return false, e
		}
	}
	row.OutputOffset = next
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE deleted=false AND node_id=? AND port=''", plan.OutputNode).Scan(&row.OutputRows); e != nil {
		return false, e
	}
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dorisDatabase("quality")+".live_"+row.ID+" WHERE deleted=false AND port='bad'").Scan(&row.RejectedRows); e != nil {
		return false, e
	}
	// A running Kafka transaction can be ahead of the committed reader boundary.
	// Only a completed stop/savepoint establishes the entire stable result boundary.
	row.CaughtUp = stopped && drained
	return drained, nil
}
func (s *Server) streamSnapshotStage(ctx context.Context, stage *governanceDorisStage, def governanceFlowExecution, plan governanceFlowPlan, report *governanceFlowReport, counts map[GovernanceFlowEdge]int64) error {
	snapshot := def.StreamSnapshot
	if snapshot == nil || !dorisObjectID.MatchString(snapshot.ID) {
		return invalid
	}
	var row GovernanceStream
	if e := s.db.WithContext(ctx).Where("id=? AND generation=? AND state='paused'", snapshot.ID, snapshot.Offset).Take(&row).Error; e != nil {
		return problem(409, "STREAM_SNAPSHOT_CHANGED", "持续任务已变化，不能生成这次快照")
	}
	table := streamResultTable(def, snapshot.ID)
	if _, e := stage.ExecContext(ctx, "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload,violations) SELECT node_id,port,ROW_NUMBER() OVER(PARTITION BY node_id,port ORDER BY row_key),payload,violations FROM "+table+" WHERE deleted=false AND node_id<>'_dm_progress'"); e != nil {
		return e
	}
	if _, e := stage.ExecContext(ctx, "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload,violations) SELECT node_id,port,ROW_NUMBER() OVER(PARTITION BY node_id,port ORDER BY row_key),payload,violations FROM "+dorisDatabase("quality")+".live_"+snapshot.ID+" WHERE deleted=false"); e != nil {
		return e
	}
	records, e := stage.QueryContext(ctx, "SELECT node_id,port,COUNT(*) FROM governance_flow_stage GROUP BY node_id,port")
	if e != nil {
		return e
	}
	defer records.Close()
	for records.Next() {
		var node, port string
		var n int64
		if e = records.Scan(&node, &port, &n); e != nil {
			return e
		}
		counts[GovernanceFlowEdge{Node: node, Port: port}] = n
		if port == "bad" {
			report.RejectedRows += n
		}
	}
	if e = records.Err(); e != nil {
		return e
	}
	report.Engine = "flink"
	report.SnapshotScope = "committed_savepoint"
	report.Nodes = []governanceFlowNodeResult{{NodeID: plan.OutputNode, OutputCount: counts[GovernanceFlowEdge{Node: plan.OutputNode}]}}
	return nil
}
func (s *Server) streamLivePage(ctx context.Context, row GovernanceStream, def governanceFlowExecution, start, size int) ([]DataRow, error) {
	if !dorisObjectID.MatchString(row.ID) || start < 0 || size < 1 || size > 100 {
		return nil, invalid
	}
	_, plan, _, e := governanceExecutionPlan(def)
	if e != nil {
		return nil, e
	}
	db, e := dorisOpen(ctx, false)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, "SELECT payload FROM "+streamResultTable(def, row.ID)+" WHERE node_id=? AND port='' AND deleted=false ORDER BY row_key LIMIT "+fmt.Sprint(size)+" OFFSET "+fmt.Sprint(start), plan.OutputNode)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DataRow{}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		record, err := decode[DataRow](raw)
		if err != nil {
			return nil, err
		}
		fields := []Field{}
		for _, field := range plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}] {
			fields = append(fields, field.Field)
		}
		if err = normalizeSnapshotNumbers(record, Object{Fields: fields}); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
