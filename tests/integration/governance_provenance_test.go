package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceOutputProvenanceAndDirectoryVariants(t *testing.T) {
	t.Setenv("DIRECTORY_TEST_MODE", "1")
	db, service, router := fixture(t)
	actorID, actorName := businessUser(t, service, "provenance_actor_", "processing-department", []string{"admin"})
	providerID, providerName := businessUser(t, service, "provenance_provider_", "original-provider", []string{"provider", "operator", "governor"})
	_, consumerName := businessUser(t, service, "provenance_consumer_", "consumer-department", []string{"consumer"})
	actor, ap := login(t, router, actorName)
	provider, pp := login(t, router, providerName)
	consumer, cp := login(t, router, consumerName)
	for _, capability := range []string{"manage_resource", "operate_resource", "govern_resource"} {
		seedAssignment(t, db, providerID, pp.DepartmentID, capability)
	}
	id := "provenance-source-" + actorID
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}
	for index, value := range []string{`{"id":1,"name":" Alice "}`, `{"id":2,"name":" Bob "}`} {
		if err := db.Create(&workflow.ImportRow{BatchID: id, RowNumber: index + 1, Data: json.RawMessage(value)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := workflow.ManagedResource{Name: "原部门客户资料", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: pp.DepartmentID, OwnerID: providerID, State: "available", Version: 1, Body: jsonBody(t, source)}).Error; err != nil {
		t.Fatal(err)
	}
	ref := workflow.Resource{Type: "table_resource", ID: id, Version: 1}
	original := seedSharingFixture(t, db, providerID, pp.DepartmentID, ref, fields)
	var directoryRevision workflow.Record
	if err := db.Where("kind='directory_revision' AND id=?", original.DirectoryRevisionID).Take(&directoryRevision).Error; err != nil {
		t.Fatal(err)
	}
	var directoryData workflow.DirectoryRevisionData
	if err := json.Unmarshal(directoryRevision.Body, &directoryData); err != nil {
		t.Fatal(err)
	}
	directoryData.Definition.DataLayer = "raw_object"
	if err := db.Model(&directoryRevision).Update("body", jsonBody(t, directoryData)).Error; err != nil {
		t.Fatal(err)
	}
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/"+path, jsonBody(t, body), actor, ap.CSRFToken, fmt.Sprintf("provenance-%d", serial))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(value["id"], &id); err != nil || id == "" {
			t.Fatal("missing record id")
		}
		return id
	}
	model := workflow.GovernanceDefinition{Name: "客户结果", OwnerID: actorID, StewardID: actorID, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}}}
	modelID := idOf(post("governance/models", map[string]interface{}{"definition": model}, 201))
	post("governance/models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: ref, Fields: []string{"id", "name"}}},
		{ID: "clean", Kind: "transform", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Columns: []workflow.GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}},
		{ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "clean"}}, Checks: []workflow.Check{{Kind: "required", Field: "id", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, Model: &workflow.GovernanceRef{ID: modelID, Version: 1}},
	}}
	flow := workflow.GovernanceDefinition{Name: "客户资料质量治理", OwnerID: actorID, StewardID: actorID, Dataflow: &graph}
	flowID := idOf(post("governance/dataflows", map[string]interface{}{"definition": flow}, 201))
	post("governance/dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
	runID := idOf(post("governance/dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var run workflow.GovernanceRun
		if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("run state=%s error=%s", run.State, run.ErrorCode)
		}
	}
	var result workflow.Record
	if err := db.Where("kind='dataset' AND id=?", runID).Take(&result).Error; err != nil {
		t.Fatal(err)
	}
	var data workflow.DatasetData
	if err := json.Unmarshal(result.Body, &data); err != nil {
		t.Fatal(err)
	}
	if result.DepartmentID != pp.DepartmentID || data.Governance == nil || !data.Governance.SingleSource || data.Governance.OriginalDirectoryID != original.DirectoryID || len(data.Governance.Steps) != 2 {
		t.Fatalf("original provider/catalog/treatments not retained: dept=%s data=%s", result.DepartmentID, result.Body)
	}
	if w := workflowRequest(router, "GET", "/api/v1/governance/datasets/"+runID, nil, actor, "", ""); w.Code != 200 {
		t.Fatalf("actor lost result visibility=%d %s", w.Code, w.Body.String())
	}
	if w := workflowRequest(router, "POST", "/api/v1/datasets/"+runID+"/publish", []byte(`{}`), provider, pp.CSRFToken, "provenance-provider-publish"); w.Code != 200 {
		t.Fatalf("original provider cannot publish=%d %s", w.Code, w.Body.String())
	}
	newRef := workflow.Resource{Type: "governed_dataset", ID: runID, Version: 1}
	mount := post("resource-mounts", workflow.MountInput{DirectoryID: original.DirectoryID, Resource: newRef, Mapping: map[string]string{"id": "id", "name": "name"}}, 201)
	var mountID, revisionID string
	if err := json.Unmarshal(mount["mount_id"], &mountID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mount["revision_id"], &revisionID); err != nil {
		t.Fatal(err)
	}
	validated := post("resource-mounts/"+mountID+"/validate", workflow.VersionAction{ExpectedVersion: 1}, 200)
	var mountVersion int
	if err := json.Unmarshal(validated["version"], &mountVersion); err != nil {
		t.Fatal(err)
	}
	// The existing review lifecycle is covered separately; this fixture approves only the new tested revision.
	if err := db.Model(&workflow.Record{}).Where("kind='mount_revision' AND id=?", revisionID).Update("state", "approved").Error; err != nil {
		t.Fatal(err)
	}
	post("resource-mounts/"+mountID+"/activate", workflow.VersionAction{ExpectedVersion: mountVersion}, 200)
	w := workflowRequest(router, "GET", "/api/v1/resource-directories/"+original.DirectoryID, nil, consumer, "", "")
	if w.Code != 200 {
		t.Fatalf("directory=%d %s", w.Code, w.Body.String())
	}
	var catalog struct {
		Resource  workflow.Resource `json:"resource"`
		Resources []struct {
			Resource        workflow.Resource              `json:"resource"`
			SharingSnapshot workflow.SharingSnapshot       `json:"sharing_snapshot"`
			Governance      *workflow.GovernanceProvenance `json:"governance"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Resource != ref || len(catalog.Resources) != 2 {
		t.Fatalf("original resource replaced or variants missing: %s", w.Body.String())
	}
	var governedSnapshot workflow.SharingSnapshot
	for _, variant := range catalog.Resources {
		if variant.Resource == newRef {
			governedSnapshot = variant.SharingSnapshot
			if variant.Governance == nil || len(variant.Governance.Steps) != 2 {
				t.Fatal("governance explanation missing")
			}
		}
	}
	if governedSnapshot.MountID != mountID || governedSnapshot.DirectoryID != original.DirectoryID || governedSnapshot == original {
		t.Fatal("governed variant must use its own mount snapshot")
	}
	w = workflowRequest(router, "GET", "/api/v1/resource-directories?data_layer=governed_dataset&search=TEST-"+id, nil, consumer, "", "")
	if w.Code != 200 {
		t.Fatalf("variant filter=%d %s", w.Code, w.Body.String())
	}
	var filtered struct {
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &filtered)
	if filtered.Total != 1 {
		t.Fatalf("governed filter lost original catalog: %s", w.Body.String())
	}
	request := workflow.GrantRequest{Resource: newRef, DepartmentID: cp.DepartmentID, Purpose: "客户核对", Fields: []string{"id"}, ExpiresAt: time.Now().Add(time.Hour), Materials: &workflow.ApplicationMaterials{LegalBasis: "内部数据协同", Scenario: "客户核对", Scope: "客户编号", Method: "query", DepartmentApprovalRef: "consumer-approved"}}
	w = workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, request), consumer, cp.CSRFToken, "provenance-consumer-apply")
	if w.Code != 201 {
		t.Fatalf("governed application=%d %s", w.Code, w.Body.String())
	}
	var application struct {
		Snapshot workflow.SharingSnapshot `json:"sharing_snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &application); err != nil {
		t.Fatal(err)
	}
	if application.Snapshot != governedSnapshot {
		t.Fatal("application used original-resource snapshot")
	}
	var unchanged workflow.Record
	if err := db.Where("kind='mount' AND id=?", original.MountID).Take(&unchanged).Error; err != nil || unchanged.State != "active" {
		t.Fatal("original mount must stay active")
	}
	t.Run("workflow_artifact_retains_original_provider_and_directory", func(t *testing.T) {
		post := func(path string, body interface{}, want int) map[string]json.RawMessage {
			t.Helper()
			serial++
			w := workflowRequest(router, "POST", "/api/v1/"+path, jsonBody(t, body), actor, ap.CSRFToken, fmt.Sprintf("provenance-%d", serial))
			if w.Code != want {
				t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
			}
			out := map[string]json.RawMessage{}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		pipeline := workflow.GovernanceDefinition{Name: "连续治理与检查", OwnerID: actorID, StewardID: actorID, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{
			{ID: "first", Dataflow: workflow.GovernanceRef{ID: flowID, Version: 1}},
			{ID: "second", Dataflow: workflow.GovernanceRef{ID: flowID, Version: 1}, DependsOn: []string{"first"}, Inputs: map[string]string{"source": "first"}},
			{ID: "quality", Kind: "quality_check", InputTask: "second", DependsOn: []string{"second"}},
			{ID: "publish", Kind: "publish_approval", InputTask: "second", DependsOn: []string{"second", "quality"}},
		}}}
		workflowID := idOf(post("governance/workflows", map[string]interface{}{"definition": pipeline}, 201))
		post("governance/workflows/"+workflowID+"/versions/1/publish", map[string]interface{}{}, 200)
		parent := idOf(post("governance/workflows/"+workflowID+"/versions/1/run", map[string]interface{}{}, 201))
		for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var approval workflow.GovernanceWorkflowTaskRun
			if err := db.Where("parent_run_id=? AND task_id='publish'", parent).Take(&approval).Error; err != nil {
				t.Fatal(err)
			}
			if approval.State == "waiting_approval" {
				break
			}
			if approval.State == "failed" || approval.State == "skipped" || time.Now().After(deadline) {
				t.Fatalf("workflow approval state=%s error=%s", approval.State, approval.ErrorCode)
			}
		}
		var task workflow.GovernanceWorkflowTaskRun
		if err := db.Where("parent_run_id=? AND task_id='second'", parent).Take(&task).Error; err != nil || task.ChildRunID == nil {
			t.Fatal("second task did not produce result")
		}
		var record workflow.Record
		if err := db.Where("kind='dataset' AND id=?", *task.ChildRunID).Take(&record).Error; err != nil {
			t.Fatal(err)
		}
		var artifact workflow.DatasetData
		if err := json.Unmarshal(record.Body, &artifact); err != nil {
			t.Fatal(err)
		}
		if record.DepartmentID != pp.DepartmentID || artifact.Governance == nil || artifact.Governance.OriginalDirectoryID != original.DirectoryID {
			t.Fatalf("workflow lost original ownership/catalog: %s", record.Body)
		}
		approval := workflowRequest(router, "POST", "/api/v1/governance/publication-approvals/"+parent+"/publish/approve", jsonBody(t, map[string]string{"dataset_id": record.ID, "note": "原提供部门核验治理成果"}), provider, pp.CSRFToken, "provenance-workflow-publish")
		if approval.Code != 200 {
			t.Fatalf("original provider cannot approve cross-department workflow: %d %s", approval.Code, approval.Body.String())
		}
	})
}
