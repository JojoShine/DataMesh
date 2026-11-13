package workflow

import (
	"context"
	"datamesh/internal/platform/coderunner"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamScriptSignatureBindsExactRequestAndExpiry(t *testing.T) {
	key := []byte(strings.Repeat("ab", 32))
	now := time.Unix(1800000000, 0)
	stamp := "1800000000"
	body := []byte(`{"stream_id":"one"}`)
	sig := streamScriptMAC(key, stamp, body)
	if !validStreamScriptMAC(key, stamp, sig, body, now) {
		t.Fatal("valid signature rejected")
	}
	for _, bad := range []struct {
		key        []byte
		stamp, sig string
		body       []byte
		now        time.Time
	}{
		{[]byte("wrong"), stamp, sig, body, now}, {key, stamp, sig, []byte(`{"stream_id":"two"}`), now}, {key, stamp, sig, body, now.Add(61 * time.Second)},
		{key, stamp, sig, body, now.Add(-61 * time.Second)}, {key, stamp, strings.ToUpper(sig), body, now}, {key, "bad", sig, body, now},
	} {
		if validStreamScriptMAC(bad.key, bad.stamp, bad.sig, bad.body, bad.now) {
			t.Fatal("tamper/expired request accepted")
		}
	}
}

func TestStreamScriptInputUsesExactFieldsAndNumbers(t *testing.T) {
	fields := []governanceFlowField{{Field: Field{Name: "id", LogicalType: "integer"}}, {Field: Field{Name: "name", LogicalType: "string", Nullable: true}}}
	row, e := streamScriptInput("script", fields, json.RawMessage(`{"name":null,"id":"9007199254740993"}`))
	if e != nil || string(row["id"]) != `"9007199254740993"` || string(row["name"]) != "null" {
		t.Fatalf("row=%s error=%v", encode(row), e)
	}
	for _, raw := range []string{`{"id":null,"name":null}`, `{"id":"1"}`, `{"id":"1","name":null,"secret":"x"}`, `{"id":"1.5","name":null}`, `{"id":"1","name":null} {}`, `[]`, `{"id":"1","name":null,"name":"shadow"}`} {
		if _, e := streamScriptInput("script", fields, json.RawMessage(raw)); e == nil {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
}

func TestStreamScriptOneRowUsesFreshBoundedRunnerAndCleanup(t *testing.T) {
	const jobID = "0123456789abcdef0123456789abcdef"
	deletes := 0
	creates := 0
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/jobs":
			creates++
			var req coderunner.CreateJob
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.TimeoutMS <= 0 || req.TimeoutMS > 30000 {
				t.Error("unbounded job")
			}
			json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
		case r.Method == "POST" && r.URL.Path == "/v1/jobs/"+jobID+"/batches":
			var req coderunner.Batch
			json.NewDecoder(r.Body).Decode(&req)
			if req.Sequence != 1 || len(req.Rows) != 1 || req.Rows[0].Ordinal != 1 {
				t.Error("not isolated one-row batch")
			}
			json.NewEncoder(w).Encode(coderunner.Reply{Sequence: 1, Rows: req.Rows})
		case r.Method == "DELETE":
			deletes++
			w.WriteHeader(204)
		default:
			t.Error("unexpected runner operation")
			w.WriteHeader(404)
		}
	}))
	defer runner.Close()
	token := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(token, []byte("private-test-token"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CODE_RUNNER_URL", runner.URL)
	t.Setenv("CODE_RUNNER_TOKEN_FILE", token)
	node := GovernanceFlowNode{ID: "script", Script: &GovernanceFlowScript{Language: "python", RuntimeVersion: "python@sha256:" + strings.Repeat("a", 64), Source: "def transform(row, context): return row", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}}}}
	for i := 0; i < 2; i++ {
		out, e := executeStreamScriptRow(context.Background(), node, map[string]json.RawMessage{}, DataRow{"id": json.RawMessage(`"9007199254740993"`)})
		if e != nil || string(out["id"]) != `9007199254740993` {
			t.Fatalf("out=%s err=%v", encode(out), e)
		}
	}
	if creates != 2 || deletes != 2 {
		t.Fatalf("fresh jobs=%d cleanup=%d", creates, deletes)
	}
}

func TestStreamScriptHTTPBoundaryBeforeDatabase(t *testing.T) {
	key := []byte(strings.Repeat("ab", 32))
	token := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(token, key, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE", token)
	router := gin.New()
	(&Server{}).registerStreamScriptBridge(router)
	for _, tc := range []struct {
		name, body string
		signed     bool
		status     int
	}{
		{"forged", `{}`, false, 403},
		{"unknown-field", `{"stream_id":"bad","extra":true}`, true, 400},
		{"duplicate-field", `{"stream_id":"a","stream_id":"b"}`, true, 400},
		{"unbound", `{"stream_id":"bad","generation":1,"node_id":"script","row_key":"key","payload":{}}`, true, 400},
		{"oversized", strings.Repeat("x", streamScriptBodyLimit+1), false, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", streamScriptPath, strings.NewReader(tc.body))
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			request.Header.Set("X-Datamesh-Script-Time", stamp)
			if tc.signed {
				request.Header.Set("X-Datamesh-Script-Signature", streamScriptMAC(key, stamp, []byte(tc.body)))
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
				t.Fatal("bridge requires bounded explicit body length")
			}
		})
	}
}

func TestStreamScriptInvalidJobIDsNeverReachJobEndpoints(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("private-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_RUNNER_TOKEN_FILE", token)
	node := GovernanceFlowNode{ID: "script", Script: &GovernanceFlowScript{Language: "python", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}}}}
	for _, id := range []string{"", "testjob", "../other", "job?x=1", "job#fragment", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		t.Run(fmt.Sprintf("id-%x", []byte(id)), func(t *testing.T) {
			var extra atomic.Int32
			runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/v1/jobs" {
					_ = json.NewEncoder(w).Encode(map[string]string{"job_id": id})
					return
				}
				extra.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer runner.Close()
			t.Setenv("CODE_RUNNER_URL", runner.URL)
			_, err := executeStreamScriptRow(context.Background(), node, nil, DataRow{"id": json.RawMessage("1")})
			var p *Problem
			if !errors.As(err, &p) || p.Code != "SCRIPT_PROTOCOL_INVALID" {
				t.Fatalf("invalid runner ID returned %v", err)
			}
			if extra.Load() != 0 {
				t.Fatalf("invalid job ID caused %d batch/cleanup requests", extra.Load())
			}
		})
	}
}

func TestStreamScriptDatetimeCanonicalization(t *testing.T) {
	node := GovernanceFlowNode{ID: "script", Script: &GovernanceFlowScript{Language: "python", Fields: []GovernanceField{{Name: "at", LogicalType: "datetime", Nullable: true}}}}
	for _, tc := range []struct {
		input, want string
	}{
		{`"2026-11-11T08:00:00.123456+08:00"`, `"2026-11-11T00:00:00.123456Z"`},
		{`"2026-11-11T08:00:00Z"`, `"2026-11-11T08:00:00.000000Z"`},
		{`"2026-11-11 08:00:00"`, `"2026-11-11T08:00:00.000000Z"`},
		{`null`, `null`},
		{`"2026-11-11T08:00:00.123456789Z"`, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			row, err := normalizeStreamScriptRow(node, DataRow{"at": json.RawMessage(tc.input)})
			if tc.want == "" {
				var p *Problem
				if !errors.As(err, &p) || p.Code != "SCRIPT_OUTPUT_INVALID" {
					t.Fatalf("submicrosecond timestamp accepted: %v", err)
				}
				return
			}
			if err != nil || string(row["at"]) != tc.want {
				t.Fatalf("datetime=%s err=%v want=%s", row["at"], err, tc.want)
			}
			// Cached output passes the same normalization without changing its instant.
			cached, err := normalizeStreamScriptRow(node, row)
			if err != nil || string(cached["at"]) != tc.want {
				t.Fatalf("cached datetime=%s err=%v", cached["at"], err)
			}
		})
	}
}

func TestStreamScriptJavascriptLargeIntegerSurvivesCachedNormalization(t *testing.T) {
	node := GovernanceFlowNode{ID: "javascript", Script: &GovernanceFlowScript{Language: "javascript", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}}}}
	row := DataRow{"id": json.RawMessage(`"9223372036854775807"`)}
	for i := 0; i < 2; i++ {
		var err error
		row, err = normalizeStreamScriptRow(node, row)
		if err != nil || string(row["id"]) != `"9223372036854775807"` {
			t.Fatalf("normalization %d lost exact JavaScript integer: row=%s err=%v", i, encode(row), err)
		}
	}
	if _, err := normalizeStreamScriptRow(node, DataRow{"id": json.RawMessage(`9223372036854775807`)}); err == nil {
		t.Fatal("unsafe numeric JavaScript output accepted")
	}
}

func TestStreamScriptDecimalSurvivesCachedNormalization(t *testing.T) {
	const decimal = `"99999999999999999999999999999999.123456"`
	for _, language := range []string{"python", "javascript"} {
		t.Run(language, func(t *testing.T) {
			node := GovernanceFlowNode{ID: "script", Script: &GovernanceFlowScript{Language: language, Fields: []GovernanceField{{Name: "amount", LogicalType: "decimal", Precision: 38, Scale: 6}}}}
			row := DataRow{"amount": json.RawMessage(decimal)}
			for i := 0; i < 2; i++ {
				var err error
				row, err = normalizeStreamScriptRow(node, row)
				if err != nil || string(row["amount"]) != decimal {
					t.Fatalf("normalization %d changed exact decimal: row=%s err=%v", i, encode(row), err)
				}
			}
		})
	}
}

func TestStreamScriptPrivateWriteDeadlineThroughGinTLS(t *testing.T) {
	key := []byte(strings.Repeat("ab", 32))
	var privateEntered, ordinaryEntered atomic.Bool
	router := gin.New()
	router.POST(streamScriptPath, func(c *gin.Context) {
		privateEntered.Store(true)
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil || !validStreamScriptMAC(key, c.GetHeader("X-Datamesh-Script-Time"), c.GetHeader("X-Datamesh-Script-Signature"), raw, time.Now()) {
			c.Status(http.StatusForbidden)
			return
		}
		if err := streamScriptWriteDeadline(c.Writer); err != nil {
			streamScriptFailure(c, err)
			return
		}
		time.Sleep(1200 * time.Millisecond)
		streamScriptReply(c, http.StatusOK, gin.H{})
	})
	router.GET("/ordinary", func(c *gin.Context) {
		ordinaryEntered.Store(true)
		time.Sleep(1200 * time.Millisecond)
		c.String(http.StatusOK, "ordinary")
	})
	server := httptest.NewUnstartedServer(router)
	server.Config.WriteTimeout = time.Second
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	request, err := http.NewRequest("POST", server.URL+streamScriptPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	request.Header.Set("X-Datamesh-Script-Time", stamp)
	request.Header.Set("X-Datamesh-Script-Signature", streamScriptMAC(key, stamp, []byte(`{}`)))
	response, err := client.Do(request)
	if !privateEntered.Load() {
		t.Fatalf("private request did not enter the handler: %v", err)
	}
	if err != nil {
		t.Fatalf("private response expired at the normal server deadline: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "{}" {
		t.Fatalf("private response status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	ordinary, err := client.Get(server.URL + "/ordinary")
	if !ordinaryEntered.Load() {
		t.Fatalf("ordinary request did not enter the handler: %v", err)
	}
	if err == nil {
		ordinary.Body.Close()
		t.Fatal("ordinary route inherited the private extended deadline")
	}
}

func TestStreamScriptWriteDeadlineFailsWhenUnsupported(t *testing.T) {
	if err := streamScriptWriteDeadline(httptest.NewRecorder()); err == nil {
		t.Fatal("unsupported deadline silently accepted")
	}
}
