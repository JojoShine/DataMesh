package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLightweightGovernanceScripts(t *testing.T) {
	if os.Getenv("TEST_CODE_RUNNER_URL") == "" {
		t.Fatal("real code runner required")
	}
	t.Setenv("CODE_RUNNER_URL", os.Getenv("TEST_CODE_RUNNER_URL"))
	t.Setenv("CODE_RUNNER_TOKEN_FILE", os.Getenv("TEST_CODE_RUNNER_TOKEN_FILE"))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "light_script_", "light-script-dept", []string{"governor"})
	cookie, actor := login(t, router, name)
	if err := db.Exec("DELETE FROM user_platform_access WHERE user_id=? AND platform<>'governance'", uid).Error; err != nil {
		t.Fatal(err)
	}
	file := lightweightTestUpload(t, router, cookie, actor.CSRFToken, "records.csv", "id,name\n0001, Alice \n0002, \n", 201)
	inventory := workflowRequest(router, "GET", "/api/v1/governance/code-runtimes", nil, cookie, "", "")
	if inventory.Code != 200 {
		t.Fatal(inventory.Body.String())
	}
	var runtimes struct {
		Items []struct {
			Language  string `json:"language"`
			Version   string `json:"runtime_version"`
			Available bool   `json:"available"`
		} `json:"items"`
	}
	if err := json.Unmarshal(inventory.Body.Bytes(), &runtimes); err != nil {
		t.Fatal(err)
	}
	if len(runtimes.Items) != 2 {
		t.Fatal("both script runtimes required")
	}
	base := "/api/v1/lightweight-governance"
	serial := 0
	post := func(path string, body interface{}) []byte {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("light-script-%s-%d", uid, serial))
		if w.Code != 201 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	for _, runtime := range runtimes.Items {
		t.Run(runtime.Language, func(t *testing.T) {
			if !runtime.Available {
				t.Fatal("runtime unavailable")
			}
			code := fmt.Sprintf("def transform(row, context):\n result = dict(row)\n result['%s'] = result['%s'].strip()\n result['quality_status'] = 'pass' if result['%s'] else 'fail'\n return result\n", file.Fields[1].Name, file.Fields[1].Name, file.Fields[1].Name)
			if runtime.Language == "javascript" {
				code = fmt.Sprintf("function transform(row, context) { const result = {...row}; result['%s'] = result['%s'].trim(); result.quality_status = result['%s'] ? 'pass' : 'fail'; return result; }", file.Fields[1].Name, file.Fields[1].Name, file.Fields[1].Name)
			}
			fields := []workflow.GovernanceField{{Name: file.Fields[0].Name, LogicalType: "string"}, {Name: file.Fields[1].Name, LogicalType: "string"}, {Name: "quality_status", LogicalType: "string"}}
			graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
				{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: file.FileID, Version: 1}, Fields: []string{file.Fields[0].Name, file.Fields[1].Name}}},
				{ID: "script", Kind: "script", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Script: &workflow.GovernanceFlowScript{Language: runtime.Language, RuntimeVersion: runtime.Version, Source: code, Fields: fields}},
				{ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "script"}}, Checks: []workflow.Check{{Field: "quality_status", Kind: "enum", Blocking: true, Parameters: map[string]json.RawMessage{"values": json.RawMessage(`["pass"]`)}}}},
				{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, InferModel: true},
			}}
			var unit lightweightTestUnit
			if err := json.Unmarshal(post(base+"/units", map[string]interface{}{"name": "脚本质量检查 " + runtime.Language, "graph": graph}), &unit); err != nil {
				t.Fatal(err)
			}
			var run workflow.GovernanceRun
			if err := json.Unmarshal(post(base+"/units/"+unit.ID+"/run", map[string]int{"expected_version": 1}), &run); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(100 * time.Millisecond) {
				w := workflowRequest(router, "GET", base+"/runs/"+run.ID, nil, cookie, "", "")
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
					t.Fatal(err)
				}
				if run.State == "succeeded" {
					break
				}
				if run.State == "failed" || time.Now().After(deadline) {
					t.Fatalf("run %s: %s", run.State, run.ErrorCode)
				}
			}
			var report struct {
				OutputRows     int                     `json:"output_rows"`
				RejectedRows   int                     `json:"rejected_rows"`
				GeneratedModel *workflow.GovernanceRef `json:"generated_model"`
			}
			if err := json.Unmarshal(run.Result, &report); err != nil {
				t.Fatal(err)
			}
			if run.WorkspaceID != "" || report.OutputRows != 1 || report.RejectedRows != 1 || report.GeneratedModel != nil {
				t.Fatalf("unexpected result: %s", run.Result)
			}
			for _, suffix := range []string{"/records", "/download", "/rejections", "/events"} {
				w := workflowRequest(router, "GET", base+"/runs/"+run.ID+suffix, nil, cookie, "", "")
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", suffix, w.Code, w.Body.String())
				}
				if suffix == "/download" && (!strings.Contains(w.Body.String(), "0001") || !strings.Contains(w.Body.String(), "Alice") || strings.Contains(w.Body.String(), "0002")) {
					t.Fatal("incorrect script CSV download")
				}
			}
		})
	}
	var workspaces int64
	if err := db.Table("governance_workspaces").Where("department_id=?", actor.DepartmentID).Count(&workspaces).Error; err != nil || workspaces != 0 {
		t.Fatalf("unexpected workspace: %d %v", workspaces, err)
	}
}
