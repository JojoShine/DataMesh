package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Ordinary mounted resources must be governable without inventing a CDC task.
func TestGovernanceResourceMetadata(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "govresources_", "govresources", []string{"governor"})
	_, otherName := businessUser(t, service, "govresources_other_", "govresources-other", []string{"governor"})
	cookie, actor := login(t, router, name)
	other, _ := login(t, router, otherName)
	seedAssignment(t, db, uid, "govresources", "govern_resource")
	fields := []workflow.Field{{Name: "customer_id", LogicalType: "integer", PrimaryKey: true}, {Name: "label", LogicalType: "string", Nullable: true}}
	kinds := []string{"table_resource", "file_resource", "api_resource"}
	for i, kind := range kinds {
		id := fmt.Sprintf("gov-resource-%s-%d", uid, i)
		resource := workflow.ManagedResource{Name: fmt.Sprintf("无需CDC-%d", i), DataLayer: "raw", Fields: fields, Adapter: []string{"excel", "upload", "http"}[i], Config: json.RawMessage(`{"private_key":"must-not-leak","ciphertext":"must-not-leak","batch_id":"internal-only"}`)}
		if kind == "file_resource" {
			resource.Fields = []workflow.Field{}
		}
		if err := db.Create(&workflow.Record{Kind: kind, ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, resource)}).Error; err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/governance/metadata/" + id
		w := workflowRequest(router, "POST", path+"/refresh", []byte(`{"source_version":1,"object_version":1}`), cookie, actor.CSRFToken, fmt.Sprint("contract-", i))
		if w.Code != 201 {
			t.Fatalf("contract metadata=%d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "internal-only") {
			t.Fatal("private resource configuration leaked")
		}
		var observation struct {
			ID     string           `json:"id"`
			Kind   string           `json:"observation_kind"`
			Fields []workflow.Field `json:"fields"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Kind != "registered_contract" || len(observation.Fields) != len(resource.Fields) {
			t.Fatalf("not a registered contract: %s", w.Body.String())
		}
		if got := workflowRequest(router, "GET", path+"/observations", nil, other, "", ""); got.Code != 403 {
			t.Fatalf("cross-dept=%d", got.Code)
		}
		annotation := workflow.GovernanceDefinition{Name: "普通资源业务注释", OwnerID: uid, StewardID: uid, Metadata: &workflow.GovernanceMetadata{ObjectID: id, ObservationID: observation.ID, Fields: []workflow.GovernanceFieldAnnotation{}}}
		w = workflowRequest(router, "POST", "/api/v1/governance/metadata-definitions", jsonBody(t, map[string]interface{}{"definition": annotation}), cookie, actor.CSRFToken, fmt.Sprint("annotation-", i))
		if w.Code != 201 {
			t.Fatalf("mounted resource annotation=%d %s", w.Code, w.Body.String())
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/metadata?search="+"%E6%97%A0%E9%9C%80CDC", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("metadata catalog=%d %s", w.Code, w.Body.String())
	}
	var catalog struct {
		Total int `json:"total"`
		Items []struct {
			Kind string `json:"resource_type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Total != 3 || len(catalog.Items) != 3 {
		t.Fatalf("missing ordinary resources: %s", w.Body.String())
	}
	for _, item := range catalog.Items {
		if item.Kind == "" {
			t.Fatal("missing resource type")
		}
	}
	if strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "internal-only") {
		t.Fatal("catalog leaked adapter internals")
	}
}
