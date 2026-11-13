package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIProxyProjectionAndBoundaries(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/rows", 302)
			return
		}
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", (2<<20)+1)))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{map[string]interface{}{"name": "Alice", "secret": "excluded"}}})
	}))
	defer upstream.Close()
	t.Setenv("API_ALLOWED_TARGETS", string(encode([]APITarget{{Origin: upstream.URL, Addresses: []string{"127.0.0.1"}}})))
	server := &Server{}
	cfg := APIConfig{URL: upstream.URL + "/rows", Method: "GET", AuthType: "none", DataPath: "data", ResponseFields: map[string]string{"customer": "name"}, Parameters: []APIParameter{{Name: "id", Type: "integer", Required: true}}}
	fields := []Field{{Name: "customer", LogicalType: "string"}}
	rows, err := server.callAPI(context.Background(), cfg, fields, map[string]interface{}{"id": "123"})
	if err != nil || len(rows) != 1 || len(rows[0]) != 1 || rows[0]["customer"] != "Alice" {
		t.Fatalf("projection: %v %v", rows, err)
	}
	before := calls
	if _, err = server.callAPI(context.Background(), cfg, fields, map[string]interface{}{"id": "x"}); err == nil || calls != before {
		t.Fatal("invalid param reached upstream")
	}
	if _, err = server.callAPI(context.Background(), cfg, fields, map[string]interface{}{"id": "1", "Authorization": "override"}); err == nil || calls != before {
		t.Fatal("unknown param accepted")
	}
	for _, path := range []string{"/redirect", "/large"} {
		cfg.URL = upstream.URL + path
		if _, err = server.callAPI(context.Background(), cfg, fields, map[string]interface{}{"id": "1"}); err == nil {
			t.Fatal("unsafe response accepted")
		}
	}
	cfg.URL = upstream.URL + "/rows"
	t.Setenv("API_ALLOWED_TARGETS", string(encode([]APITarget{{Origin: upstream.URL, Addresses: []string{"192.0.2.1"}}})))
	if _, err = server.callAPI(context.Background(), cfg, fields, map[string]interface{}{"id": "1"}); err == nil {
		t.Fatal("address mismatch accepted")
	}
}

func TestAPIAuthenticatedPost(t *testing.T) {
	runtime := &Runtime{key: []byte("01234567890123456789012345678901")}
	cipher, err := runtime.seal(Credential{Password: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "denied", 403)
			return
		}
		var params map[string]string
		if json.NewDecoder(r.Body).Decode(&params) != nil || params["code"] != "001" {
			http.Error(w, "bad params", 400)
			return
		}
		w.Write([]byte(`{"result":{"code":"001"}}`))
	}))
	defer upstream.Close()
	t.Setenv("API_ALLOWED_TARGETS", string(encode([]APITarget{{Origin: upstream.URL, Addresses: []string{"127.0.0.1"}}})))
	cfg := APIConfig{URL: upstream.URL, Method: "POST", QueryOnly: true, AuthType: "bearer", Ciphertext: cipher, DataPath: "result", ResponseFields: map[string]string{"code": "code"}, Parameters: []APIParameter{{Name: "code", Type: "string", Required: true}}}
	rows, err := (&Server{runtime: runtime}).callAPI(context.Background(), cfg, []Field{{Name: "code", LogicalType: "string"}}, map[string]interface{}{"code": "001"})
	if err != nil || rows[0]["code"] != "001" {
		t.Fatalf("post %v %v", rows, err)
	}
}
