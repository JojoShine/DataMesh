package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestLightweightQualityFormats(t *testing.T) {
	_, service, router := fixture(t)
	_, name := businessUser(t, service, "quality_formats_", "quality-formats", []string{"governor"})
	cookie, p := login(t, router, name)
	file := lightweightTestUpload(t, router, cookie, p.CSRFToken, "联系方式.csv", "mobile,email,date\n13800138000,reader@example.org,2026-11-09\n123,broken,2026/11/09\n", 201)
	fields := []string{}
	checks := []workflow.Check{}
	for i, format := range []string{"mobile", "email", "date"} {
		fields = append(fields, file.Fields[i].Name)
		checks = append(checks, workflow.Check{Kind: "format", Field: file.Fields[i].Name, Blocking: true, Parameters: map[string]json.RawMessage{"format": jsonBody(t, format)}})
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: file.FileID, Version: 1}, Fields: fields}}, {ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Checks: checks}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, InferModel: true}}}
	base := "/api/v1/lightweight-governance"
	post := func(path string, body interface{}) []byte {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 201 {
			t.Fatalf("%s %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var unit lightweightTestUnit
	if e := json.Unmarshal(post(base+"/units", map[string]interface{}{"name": "联系方式格式检查", "graph": graph}), &unit); e != nil {
		t.Fatal(e)
	}
	var run workflow.GovernanceRun
	if e := json.Unmarshal(post(base+"/units/"+unit.ID+"/run", map[string]int{"expected_version": 1}), &run); e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		w := workflowRequest(router, "GET", base+"/runs/"+run.ID, nil, cookie, "", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
			t.Fatal(w.Body)
		}
		if run.State == "succeeded" {
			var report struct {
				Output   int `json:"output_rows"`
				Rejected int `json:"rejected_rows"`
			}
			if e := json.Unmarshal(run.Result, &report); e != nil || report.Output != 1 || report.Rejected != 1 {
				t.Fatalf("format result %s", run.Result)
			}
			break
		}
		if run.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("format run %s %s", run.State, run.ErrorCode)
		}
	}
}
