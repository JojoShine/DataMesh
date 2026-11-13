package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"datamesh/internal/platform/httpserver"
)

func TestSourceAllowlistAdministration(t *testing.T) {
	t.Setenv("SOURCE_ALLOWED_TARGETS", `[{"kind":"mysql","host":"env-db.internal","port":3306,"database":"business"}]`)
	db, service, router := fixture(t, false)
	_, adminName := businessUser(t, service, "allow_admin_", "admin-dept", []string{"admin"})
	_, operatorName := businessUser(t, service, "allow_operator_", "ops-dept", []string{"operator"})
	cookie, p := login(t, router, adminName)
	operator, op := login(t, router, operatorName)
	const path = "/api/v1/source-allowed-targets"
	if w := workflowRequest(router, "GET", path, nil, cookie, "", ""); w.Code != 200 {
		t.Fatalf("list=%d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{"GET", "POST"} {
		if w := workflowRequest(router, method, path, []byte(`{}`), operator, op.CSRFToken, "non-admin-"+method); w.Code != 403 {
			t.Fatalf("operator %s=%d", method, w.Code)
		}
	}
	u, err := url.Parse(os.Getenv("TEST_APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := map[string]interface{}{"kind": "postgres", "host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/")}
	pass, _ := u.User.Password()
	draft := workflow.SourceInput{Kind: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}
	if w := workflowRequest(router, "POST", "/api/v1/sources/test-connection", jsonBody(t, draft), cookie, p.CSRFToken, "before-add"); w.Code != 403 {
		t.Fatalf("unapproved=%d", w.Code)
	}
	if w := workflowRequest(router, "POST", path, jsonBody(t, endpoint), cookie, "", "missing-csrf"); w.Code != 403 {
		t.Fatalf("csrf=%d", w.Code)
	}
	for i, bad := range []string{`{"kind":"postgres","host":"*","port":5432,"database":"business"}`, `{"kind":"postgres","host":"https://db.internal","port":5432,"database":"business"}`, `{"kind":"postgres","host":"db.internal","port":0,"database":"business"}`, `{"kind":"sqlite","host":"db.internal","port":5432,"database":"business"}`, `{"kind":"mysql","host":"db.internal","port":3306,"database":"business","password":"must-not-be-stored"}`} {
		if w := workflowRequest(router, "POST", path, []byte(bad), cookie, p.CSRFToken, fmt.Sprint("invalid-", i)); w.Code != 400 {
			t.Fatalf("invalid=%d", w.Code)
		}
	}
	added := workflowRequest(router, "POST", path, jsonBody(t, endpoint), cookie, p.CSRFToken, "allow-add")
	if added.Code != 201 {
		t.Fatalf("add=%d %s", added.Code, added.Body.String())
	}
	var item struct {
		ID string `json:"target_id"`
	}
	if err = json.Unmarshal(added.Body.Bytes(), &item); err != nil || item.ID == "" {
		t.Fatal("missing target ID")
	}
	if w := workflowRequest(router, "POST", path, jsonBody(t, endpoint), cookie, p.CSRFToken, "allow-add"); w.Code != 201 {
		t.Fatalf("idempotency status=%d", w.Code)
	} else {
		var replay struct {
			ID string `json:"target_id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &replay); err != nil || replay.ID != item.ID {
			t.Fatal("idempotency created a different target")
		}
	}
	if w := workflowRequest(router, "POST", path, jsonBody(t, endpoint), cookie, p.CSRFToken, "duplicate"); w.Code != 409 {
		t.Fatalf("duplicate=%d", w.Code)
	}
	for _, kind := range []string{"mysql", "oracle"} {
		other := map[string]interface{}{"kind": kind, "host": "approved.internal", "port": 3306, "database": "business"}
		if w := workflowRequest(router, "POST", path, jsonBody(t, other), cookie, p.CSRFToken, kind); w.Code != 201 {
			t.Fatalf("kind %s=%d", kind, w.Code)
		}
	}
	second, stop, err := httpserver.NewWithLifecycle(context.Background(), db, service, origin)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	w := workflowRequest(second, "POST", "/api/v1/sources/test-connection", jsonBody(t, draft), cookie, p.CSRFToken, "after-add")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"passed":true`) {
		t.Fatalf("instant persistent connection=%d %s", w.Code, w.Body.String())
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.SetMaxOpenConns(10)
	draft.Name = "whitelisted database"
	draft.OwnerID = p.UserID
	w = workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, draft), cookie, p.CSRFToken, "create-whitelisted")
	if w.Code != 201 {
		t.Fatalf("source create=%d %s", w.Code, w.Body.String())
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if w = workflowRequest(router, "POST", "/api/v1/sources/"+source.ID+"/test-connection", []byte(`{}`), cookie, p.CSRFToken, "saved-source-check"); w.Code != 200 || !strings.Contains(w.Body.String(), `"passed":true`) {
		t.Fatalf("saved source with one connection=%d %s", w.Code, w.Body.String())
	}
	if w = workflowRequest(router, "PATCH", "/api/v1/sources/"+source.ID, jsonBody(t, draft), cookie, p.CSRFToken, "update-whitelisted"); w.Code != 200 {
		t.Fatalf("update with one connection=%d %s", w.Code, w.Body.String())
	}
	remove := path + "/" + item.ID + "/remove"
	if w = workflowRequest(router, "POST", remove, []byte(`{}`), operator, op.CSRFToken, "operator-remove"); w.Code != 403 {
		t.Fatalf("operator remove=%d", w.Code)
	}
	if w = workflowRequest(router, "POST", remove, []byte(`{}`), cookie, p.CSRFToken, "remove-allow"); w.Code != 200 {
		t.Fatalf("remove=%d %s", w.Code, w.Body.String())
	}
	if w = workflowRequest(second, "POST", "/api/v1/sources/test-connection", jsonBody(t, draft), cookie, p.CSRFToken, "after-remove"); w.Code != 403 {
		t.Fatalf("revocation=%d", w.Code)
	}
	if w = workflowRequest(second, "GET", "/api/v1/sources/"+source.ID+"/tables", nil, cookie, "", ""); w.Code != 403 {
		t.Fatalf("discovery after revocation=%d %s", w.Code, w.Body.String())
	}
	var sourceCount int64
	if err = db.Model(&workflow.Record{}).Where("kind='source' AND id=?", source.ID).Count(&sourceCount).Error; err != nil || sourceCount != 1 {
		t.Fatal("revocation deleted source")
	}
	listed := workflowRequest(router, "GET", path, nil, cookie, "", "")
	var list struct {
		Items []struct {
			ID     string `json:"target_id"`
			Origin string `json:"origin"`
		}
	}
	if err = json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	envSeen, builtinSeen := false, false
	for _, v := range list.Items {
		if v.ID == item.ID {
			t.Fatal("revoked item still listed")
		}
		if v.Origin == "environment" {
			envSeen = true
		}
		if v.Origin == "builtin" {
			builtinSeen = true
		}
		if v.Origin != "platform" {
			if w = workflowRequest(router, "POST", path+"/"+v.ID+"/remove", []byte(`{}`), cookie, p.CSRFToken, "readonly-"+v.ID); w.Code != 409 {
				t.Fatalf("readonly=%d", w.Code)
			}
		}
	}
	if !envSeen || !builtinSeen {
		t.Fatal("static allowlist missing")
	}
	var records []identity.AuditRecord
	if err = db.Where("object_id=? AND result='success'", item.ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, r := range records {
		actions[r.Action] = true
		if r.ActorID == nil || *r.ActorID != p.UserID || r.TraceID == "" {
			t.Fatal("incomplete audit")
		}
	}
	if len(records) != 2 || !actions["add_source_allowed_target"] || !actions["remove_source_allowed_target"] {
		t.Fatalf("audit=%v", actions)
	}
	if w = workflowRequest(router, "POST", path, jsonBody(t, endpoint), cookie, p.CSRFToken, "re-add-target"); w.Code != 201 {
		t.Fatalf("re-add=%d", w.Code)
	}
	if strings.Contains(listed.Body.String(), pass) {
		t.Fatal("allowlist leaked credentials")
	}
}
