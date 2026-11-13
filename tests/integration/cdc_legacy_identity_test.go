package integration

import (
	"database/sql"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCDCLegacyInstanceIdentity(t *testing.T) {
	local, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	remote, e := url.Parse(os.Getenv("CDC_TEST_SOURCE_DSN"))
	if e != nil || remote.Host == "" || local.Path != remote.Path {
		t.Fatal("two isolated instances with matching database names required")
	}
	allowed := []map[string]interface{}{}
	for _, u := range []*url.URL{local, remote} {
		port, _ := strconv.Atoi(u.Port())
		allowed = append(allowed, map[string]interface{}{"kind": "postgres", "host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/")})
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, allowed)))
	db, service, router := fixture(t, false)
	remoteDB, e := sql.Open("pgx", remote.String())
	if e != nil {
		t.Fatal(e)
	}
	defer remoteDB.Close()
	if e = db.Exec("CREATE TABLE public.customers(id bigint PRIMARY KEY,name text)").Error; e != nil {
		t.Fatal(e)
	}
	if _, e = remoteDB.Exec("CREATE TABLE public.customers(id bigint PRIMARY KEY,name text)"); e != nil {
		t.Fatal(e)
	}
	uid, name := businessUser(t, service, "legacy_identity_", "test", []string{"admin"})
	cookie, p := login(t, router, name)
	register := func(u *url.URL, key string) string {
		t.Helper()
		port, _ := strconv.Atoi(u.Port())
		password, _ := u.User.Password()
		w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, workflow.SourceInput{Name: key, Kind: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), OwnerID: uid, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}), cookie, p.CSRFToken, key)
		if w.Code != 201 {
			t.Fatalf("source HTTP %d", w.Code)
		}
		var source struct {
			ID string `json:"source_id"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &source); e != nil {
			t.Fatal(e)
		}
		return source.ID
	}
	localID := register(local, "legacy-instance")
	remoteID := register(remote, "independent-instance")
	aliasID := register(local, "same-instance-alias")
	legacy := workflow.Record{Kind: "task", ID: "legacy-instance-task", OwnerID: uid, ParentID: localID, State: "draft", Version: 1, Body: jsonBody(t, workflow.TaskData{TaskInput: workflow.TaskInput{SourceID: localID, ObjectID: "legacy-customers", SchemaVersion: 1}, Binding: "postgres"})}
	if e = db.Create(&legacy).Error; e != nil {
		t.Fatal(e)
	}
	for _, input := range []struct {
		ID, Key string
		Status  int
	}{{remoteID, "independent-capture", 201}, {aliasID, "same-capture", 409}} {
		w := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, map[string]interface{}{"source_id": input.ID, "source_version": 1, "schema": "public", "table": "customers", "publication": "identity_publication"}), cookie, p.CSRFToken, input.Key)
		if w.Code != input.Status {
			t.Errorf("%s HTTP %d, expected %d: %s", input.Key, w.Code, input.Status, w.Body.String())
		}
	}
}
