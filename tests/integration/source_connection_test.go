package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSourceDraftConnection(t *testing.T) {
	parsed, err := url.Parse(os.Getenv("TEST_APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": parsed.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t)
	_, name := businessUser(t, service, "draft_admin_", "draft-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	pass, _ := parsed.User.Password()
	input := workflow.SourceInput{Kind: "postgres", Host: parsed.Hostname(), Port: port, Database: database, Credentials: workflow.Credential{Username: parsed.User.Username(), Password: pass}}
	var before, after int64
	if err = db.Model(&workflow.Record{}).Where("kind IN ('source','operation')").Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{true, false} {
		if !success {
			input.Credentials.Password = "incorrect-test-password"
		}
		w := workflowRequest(router, "POST", "/api/v1/sources/test-connection", jsonBody(t, input), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 200 {
			t.Fatalf("test connection status %d: %s", w.Code, w.Body.String())
		}
		var result struct {
			Passed bool `json:"passed"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Passed != success {
			t.Fatal("connection result did not reflect actual credentials")
		}
		if strings.Contains(w.Body.String(), input.Credentials.Password) {
			t.Fatal("credentials leaked")
		}
	}
	input.Host = "unapproved.internal"
	w := workflowRequest(router, "POST", "/api/v1/sources/test-connection", jsonBody(t, input), cookie, p.CSRFToken, "denied")
	if w.Code != 403 {
		t.Fatalf("unapproved target accepted: %d", w.Code)
	}
	if err = db.Model(&workflow.Record{}).Where("kind IN ('source','operation')").Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("draft test persisted records")
	}
}
