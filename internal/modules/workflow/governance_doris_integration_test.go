package workflow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDorisGovernanceActualEngine(t *testing.T) {
	if os.Getenv("DATAMESH_DORIS_TEST") != "1" {
		t.Skip("requires the dedicated Doris test runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id, e := newID()
	if e != nil {
		t.Fatal(e)
	}
	stage, e := newGovernanceDorisStage(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	defer stage.close()
	defer func() {
		for _, table := range []string{stage.table, stage.ods, dorisDatabase("dwd") + ".result_" + id, dorisDatabase("quality") + ".rejected_" + id} {
			if _, err := stage.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
				t.Error(err)
			}
		}
	}()
	precision, scale := 20, 6
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Nullable: true}, {Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale, Nullable: true}}
	if _, e = stage.ExecContext(ctx, "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload) VALUES ($1,'',1,$2),($1,'',2,$3),($1,'',3,$4)", "source", `{"id":1,"name":" A ","amount":"9007199254740.123456"}`, `{"id":2,"name":null,"amount":"0.000001"}`, `{"id":3,"name":" B ","amount":null}`); e != nil {
		t.Fatal(e)
	}
	plan := governanceFlowPlan{Outputs: map[GovernanceFlowEdge][]governanceFlowField{}}
	ff := []governanceFlowField{}
	for _, f := range fields {
		ff = append(ff, governanceFlowField{Field: f})
	}
	plan.Outputs[GovernanceFlowEdge{Node: "source"}] = ff
	nodes := []GovernanceFlowNode{
		{ID: "clean", Kind: "transform", Inputs: []GovernanceFlowEdge{{Node: "source"}}, Columns: []GovernanceFlowColumn{{Source: "id", Target: "id"}, {Source: "name", Target: "name", Rule: "trim"}, {Source: "amount", Target: "amount"}}},
		{ID: "quality", Kind: "quality", Inputs: []GovernanceFlowEdge{{Node: "clean"}}, Checks: []Check{{Kind: "required", Field: "name", Blocking: true}, {Kind: "type", Field: "amount", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "quality", Port: "good"}}},
	}
	for _, n := range nodes {
		plan.Outputs[GovernanceFlowEdge{Node: n.ID}] = ff
		plan.Outputs[GovernanceFlowEdge{Node: n.ID, Port: "good"}] = ff
		query, args, err := governanceDorisSelect(n, plan)
		if err != nil {
			t.Fatal(err)
		}
		args = append(args, n.ID)
		sql := "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload,violations) SELECT $" + fmt.Sprint(len(args)) + ",port,ordinal,payload,violations FROM (" + query + ") result"
		if _, e = stage.ExecContext(ctx, sql, args...); e != nil {
			t.Fatalf("%s: %v", n.Kind, e)
		}
	}
	var rejected int
	if e = stage.QueryRowContext(ctx, "SELECT COUNT(*) FROM governance_flow_stage WHERE port='bad'").Scan(&rejected); e != nil || rejected != 1 {
		t.Fatalf("quality rejected=%d err=%v", rejected, e)
	}
	binding, e := stage.publish(ctx, id, "output", "dwd", fields)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := dorisResultPage(ctx, binding, id, fields, 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || flowLiteralText(rows[0]["name"]) != "A" || !strings.Contains(flowLiteralText(rows[0]["amount"]), "9007199254740.123456") || string(rows[1]["amount"]) != "null" {
		t.Fatalf("wrong output: %s", encode(rows))
	}
}
