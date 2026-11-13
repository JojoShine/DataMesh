package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"datamesh/internal/modules/workflow"
)

func TestSourceMetadataKeepsConnection(t *testing.T) {
	db, service, router := fixture(t, false)
	ownerID, ownerName := businessUser(t, service, "metadata_", "metadata-source-dept", []string{"admin"})
	_, readerName := businessUser(t, service, "metareader_", "metadata-source-dept", []string{"consumer"})
	owner, p := login(t, router, ownerName)
	reader, rp := login(t, router, readerName)
	initial := workflow.SourceSecret{Name: "原连接名称", Kind: "postgres", Host: "source.internal", Port: 5432, Database: "business", Ciphertext: "opaque-encrypted-credential", ConnectionStatus: "connected", CDCStatus: "ready"}
	id := "metadata-source-" + ownerID
	record := workflow.Record{ID: id, Kind: "source", DepartmentID: p.DepartmentID, OwnerID: p.UserID, State: "registered", Version: 1, Body: jsonBody(t, initial)}
	if e := db.Create(&record).Error; e != nil {
		t.Fatal(e)
	}
	body := jsonBody(t, map[string]interface{}{"name": "业务来源库", "expected_version": 1, "reason": "规范业务展示名称"})
	if w := workflowRequest(router, "POST", "/api/v1/sources/"+id+"/metadata", body, reader, rp.CSRFToken, "metadata-reader"); w.Code != 403 {
		t.Fatalf("reader=%d", w.Code)
	}
	w := workflowRequest(router, "POST", "/api/v1/sources/"+id+"/metadata", body, owner, p.CSRFToken, "metadata-owner")
	if w.Code != 200 {
		t.Fatalf("rename=%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), initial.Ciphertext) {
		t.Fatal("credential exposed")
	}
	var stored workflow.Record
	if e := db.Where("id=?", id).Take(&stored).Error; e != nil {
		t.Fatal(e)
	}
	var updated workflow.SourceSecret
	if e := json.Unmarshal(stored.Body, &updated); e != nil {
		t.Fatal(e)
	}
	if updated.Name != "业务来源库" || updated.Host != initial.Host || updated.Database != initial.Database || updated.Ciphertext != initial.Ciphertext || updated.ConnectionStatus != initial.ConnectionStatus || stored.Version != 2 {
		t.Fatal("metadata edit changed connection")
	}
}
