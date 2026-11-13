package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func TestGovernanceDatasetDetailScope(t *testing.T) {
	db, service, router := fixture(t, false)
	name := fmt.Sprintf("govdetail_%d", time.Now().UnixNano())
	uid, err := service.CreateUser(context.Background(), identity.NewUser{Username: name, Password: password, DepartmentID: "govdetail-main", Platforms: []string{"governance"}, Roles: []string{"governor"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie, actor := login(t, router, name)
	_, otherName := businessUser(t, service, "govdetail_other_", "govdetail-other", []string{"governor"})
	other, _ := login(t, router, otherName)
	_, consumerName := businessUser(t, service, "govdetail_consumer_", "govdetail-main", []string{"consumer"})
	consumer, _ := login(t, router, consumerName)
	id := "govdetail-" + uid
	data := workflow.DatasetData{Definition: workflow.DatasetInput{Name: "治理可见成果", Description: "只读详情不改变发布职责", OwnerID: uid, Fields: []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}}, QualityStatus: "not_checked", CompletenessStatus: "pending"}
	if err = db.Create(&workflow.Record{Kind: "dataset", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "draft", Version: 1, Body: jsonBody(t, data)}).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/governance/datasets/" + id
	for _, item := range []struct {
		path   string
		cookie *http.Cookie
		status int
	}{{path, cookie, 200}, {"/api/v1/datasets/" + id, cookie, 403}, {path, other, 403}, {path, consumer, 403}} {
		response := request(router, "GET", item.path, "", item.cookie, "", "")
		if response.Code != item.status {
			t.Fatalf("detail scope: got %d, expected %d", response.Code, item.status)
		}
	}
	response := workflowRequest(router, "POST", "/api/v1/datasets/"+id+"/publish", []byte("{}"), cookie, actor.CSRFToken, "govdetail-publish")
	if response.Code != 403 {
		t.Fatalf("governance visibility must not grant publication: %d", response.Code)
	}
	if err = db.Model(&workflow.Record{}).Where("kind='dataset' AND id=?", id).Update("state", "published").Error; err != nil {
		t.Fatal(err)
	}
	if response = request(router, "GET", path, "", other, "", ""); response.Code != 200 {
		t.Fatalf("published visibility must match the existing governance list: %d", response.Code)
	}
}
