package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceReaderProfiles(t *testing.T) {
	if len(sourceTypes) != 14 {
		t.Fatal("source range changed")
	}
	seen := map[string]bool{}
	for _, v := range sourceTypes {
		if seen[v.Kind] || v.Name == "" || v.Port < 1 {
			t.Fatal("invalid source profile")
		}
		seen[v.Kind] = true
		if v.Reader && (v.CDC || v.Write || v.MaxRows != 10000) {
			t.Fatal("unverified write or CDC capability")
		}
		database := "business"
		if v.Kind == "redis" {
			database = "0"
		}
		if !validSourceTarget(SourceTarget{Kind: v.Kind, Host: "db.internal", Port: v.Port, Database: database}) {
			t.Fatalf("target rejected: %s", v.Kind)
		}
	}
	for _, excluded := range []string{"tidb", "oceanbase", "opengauss", "gaussdb", "gbase", "oscar"} {
		if _, ok := sourceType(excluded); ok {
			t.Fatal("unapproved type")
		}
	}
}
func TestSourceReaderRequestBoundary(t *testing.T) {
	token := strings.Repeat("private-reader-token", 3)
	t.Setenv("SOURCE_READER_TOKEN", token)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing reader authentication")
		}
		if r.URL.Path != "/v1/source" {
			t.Error("unexpected reader route")
		}
		var input map[string]interface{}
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			t.Error(e)
		}
		if input["kind"] != "redis" || input["max_rows"] != float64(10000) {
			t.Error("incorrect kind or read limit")
		}
		if _, ok := input["sql"]; ok {
			t.Error("arbitrary SQL forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"records\":[{\"n\":9007199254740993}],\"total\":1}"))
	}))
	defer server.Close()
	t.Setenv("SOURCE_READER_ORIGIN", server.URL)
	t.Setenv("SOURCE_ALLOWED_TARGETS", `[{"kind":"redis","host":"approved.internal","port":6379,"database":"0"}]`)
	rt := &Runtime{key: bytes.Repeat([]byte{1}, 32)}
	cipher, e := rt.seal(Credential{})
	if e != nil {
		t.Fatal(e)
	}
	out, e := rt.sourceReader(context.Background(), nil, SourceSecret{Kind: "redis", Host: "approved.internal", Port: 6379, Database: "0", Ciphertext: cipher}, "read", "0", "keys")
	if e != nil {
		t.Fatal(e)
	}
	if out.Records[0]["n"] != json.Number("9007199254740993") {
		t.Fatal("integer precision lost")
	}
}
func TestSourceReaderFailsClosed(t *testing.T) {
	t.Setenv("SOURCE_READER_TOKEN", strings.Repeat("x", 40))
	for _, test := range []struct {
		status     int
		body, code string
	}{
		{422, `{"code":"SOURCE_DRIVER_MISSING"}`, "SOURCE_DRIVER_MISSING"},
		{422, `{"code":"SOURCE_READ_LIMIT"}`, "SOURCE_READ_LIMIT"},
		{200, `not-json`, "SOURCE_READER_RESPONSE_INVALID"},
		{302, `{}`, "SOURCE_UNAVAILABLE"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); w.Write([]byte(test.body)) }))
		t.Setenv("SOURCE_READER_ORIGIN", server.URL)
		var out readerResponse
		e := readerRequest(context.Background(), "/v1/source", map[string]string{"action": "probe"}, &out)
		server.Close()
		p, ok := e.(*Problem)
		if !ok || p.Code != test.code {
			t.Fatalf("unexpected failure: %v", e)
		}
	}
	t.Setenv("SOURCE_READER_ORIGIN", "http://user:password@internal")
	var out readerResponse
	if e := readerRequest(context.Background(), "/v1/capabilities", nil, &out); e == nil {
		t.Fatal("credential-bearing control origin allowed")
	}
}

func TestSourceReaderOptionalCredentials(t *testing.T) {
	for _, kind := range []string{"mongodb", "redis", "elasticsearch", "hbase", "clickhouse"} {
		if !validSourceCredential(kind, Credential{}) {
			t.Fatalf("anonymous %s rejected", kind)
		}
	}
	if validSourceCredential("mongodb", Credential{Password: "ignored"}) || validSourceCredential("mongodb", Credential{Username: "reader"}) || validSourceCredential("redis", Credential{Username: "reader"}) {
		t.Fatal("partial credentials accepted")
	}
	if !validSourceCredential("redis", Credential{Password: "configured"}) {
		t.Fatal("Redis password-only authentication rejected")
	}
}
