package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFlinkSessionUsesDetachedExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Properties map[string]string `json:"properties"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Properties["execution.attached"] != "false" {
			t.Error("remote job must outlive gateway session")
		}
		w.Write([]byte(`{"sessionHandle":"` + strings.Repeat("a", 32) + `"}`))
	}))
	defer server.Close()
	f := governanceFlink{client: server.Client(), gateway: server.URL}
	if _, err := f.session(context.Background(), "test", ""); err != nil {
		t.Fatal(err)
	}
}

func TestFlinkStatementClosesFailedOperationAfterCancellation(t *testing.T) {
	session, op := strings.Repeat("a", 32), strings.Repeat("b", 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"operationHandle":"` + op + `"}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			cancel()
			w.Write([]byte(`{"status":"RUNNING"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sessions/"+session+"/operations/"+op+"/close":
			closed <- struct{}{}
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	f := governanceFlink{client: server.Client(), gateway: server.URL}
	if err := f.statement(ctx, session, "CREATE TEMPORARY TABLE x (id INT)"); err == nil {
		t.Fatal("expected cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("failed operation retained after request cancellation")
	}
}

func TestFlinkCloseSessionAfterCancellationAndIdleExpiry(t *testing.T) {
	for _, status := range []int{200, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/sessions/"+strings.Repeat("a", 32) {
					t.Errorf("unexpected cleanup %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			f := governanceFlink{client: server.Client(), gateway: server.URL}
			err := f.closeSession(ctx, strings.Repeat("a", 32))
			if (err != nil) != (status == 503) || requests != 1 {
				t.Fatalf("cleanup status %d: %v, requests %d", status, err, requests)
			}
			if err := f.closeSession(ctx, "invalid"); err == nil || requests != 1 {
				t.Fatal("invalid session reached gateway")
			}
		})
	}
}

func TestFlinkReconciledSubmissionReleasesOnlyOwnedSession(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			id, session := strings.Repeat("a", 32), strings.Repeat("b", 32)
			closed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/jobs/overview":
					w.Write([]byte(`{"jobs":[{"jid":"` + id + `","name":"` + streamJobName(id, 1) + `","state":"FINISHED"}]}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/v1/sessions/"+session:
					closed = true
					w.WriteHeader(status)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("GOVERNANCE_FLINK_CLUSTER", server.URL)
			t.Setenv("GOVERNANCE_FLINK_GATEWAY", server.URL)
			s := &Server{runtime: &Runtime{client: server.Client()}}
			row := GovernanceStream{ID: id, Generation: 1, State: "unknown", SubmissionStarted: true, SessionID: session, OperationID: id, Sources: encode(map[string]governanceStreamSource{})}
			err := s.streamStep(context.Background(), &row, governanceFlowExecution{})
			if !closed || (err != nil) != (status == 503) {
				t.Fatalf("cleanup missing or error swallowed: %v", err)
			}
			if status == 200 && (row.SessionID != "" || row.OperationID != "" || row.State != "failed") {
				t.Fatal("confirmed cleanup not persisted")
			}
			if status == 503 && (row.SessionID != session || row.State != "unknown") {
				t.Fatal("failed cleanup lost retry state")
			}
		})
	}
}

func TestFlinkPermanentSetupFailureRetriesCleanupBeforeTerminalState(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(fmt.Sprint("submitted=", submitted), func(t *testing.T) {
			id, session := strings.Repeat("a", 32), strings.Repeat("b", 32)
			deleteStatus, deletes := http.StatusServiceUnavailable, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/jobs/overview":
					w.Write([]byte(`{"jobs":[{"jid":"` + id + `","name":"` + streamJobName(id, 1) + `","state":"FINISHED"}]}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/statements"):
					w.Write([]byte(`{"operationHandle":"` + id + `"}`))
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
					w.Write([]byte(`{"status":"ERROR"}`))
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/close"):
					w.Write([]byte(`{}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/v1/sessions/"+session:
					deletes++
					w.WriteHeader(deleteStatus)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("GOVERNANCE_FLINK_GATEWAY", server.URL)
			t.Setenv("GOVERNANCE_FLINK_CLUSTER", server.URL)
			s := &Server{runtime: &Runtime{client: server.Client()}}
			f := s.flink()
			setupErr := f.statement(context.Background(), session, "CREATE TEMPORARY TABLE invalid")
			var failure *Problem
			if !errors.As(setupErr, &failure) || failure.Status != 409 {
				t.Fatalf("expected permanent setup failure: %v", setupErr)
			}
			cleanupErr := f.closeSession(context.Background(), session)
			combined := errors.Join(setupErr, cleanupErr)
			if !errors.As(combined, &failure) || failure.Status != 409 {
				t.Fatalf("original setup failure was lost: %v", combined)
			}
			row := GovernanceStream{ID: id, Generation: 1, State: "starting", DesiredState: "failed", SubmissionStarted: submitted, SessionID: session, OperationID: id}
			if err := s.streamConfirmTermination(context.Background(), &row); err == nil {
				t.Fatal("failed cleanup incorrectly finalized an unreleased session")
			}
			if row.State == "failed" || row.SessionID != session {
				t.Fatal("session cleanup lost its retry state")
			}
			row.State = "unknown" // streamTick keeps termination errors eligible for reconciliation.
			deleteStatus = http.StatusOK
			if err := s.streamConfirmTermination(context.Background(), &row); err != nil {
				t.Fatal(err)
			}
			if row.State != "failed" || row.SessionID != "" || row.OperationID != "" || deletes != 3 {
				t.Fatal("cleanup retry did not release the owned session before terminal state")
			}
		})
	}
}

func TestFlinkTerminationRetainsSessionUntilJobTerminationConfirmed(t *testing.T) {
	for _, state := range []string{"", "RUNNING"} {
		t.Run("job="+state, func(t *testing.T) {
			id, session := strings.Repeat("a", 32), strings.Repeat("b", 32)
			canceled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/jobs/overview":
					if state == "" {
						w.Write([]byte(`{"jobs":[]}`))
					} else {
						w.Write([]byte(`{"jobs":[{"jid":"` + id + `","name":"` + streamJobName(id, 1) + `","state":"` + state + `"}]}`))
					}
				case r.Method == http.MethodPatch && r.URL.Path == "/jobs/"+id && r.URL.Query().Get("mode") == "cancel":
					canceled = true
					w.Write([]byte(`{}`))
				default:
					t.Errorf("premature cleanup or unrelated job request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("GOVERNANCE_FLINK_GATEWAY", server.URL)
			t.Setenv("GOVERNANCE_FLINK_CLUSTER", server.URL)
			s := &Server{runtime: &Runtime{client: server.Client()}}
			row := GovernanceStream{ID: id, Generation: 1, State: "unknown", DesiredState: "failed", SubmissionStarted: true, SessionID: session, OperationID: id}
			err := s.streamConfirmTermination(context.Background(), &row)
			if (err != nil) != (state == "") || canceled != (state == "RUNNING") || row.State != "unknown" || row.SessionID != session || row.OperationID != id {
				t.Fatalf("uncertain termination released resources prematurely: %v", err)
			}
		})
	}
}
