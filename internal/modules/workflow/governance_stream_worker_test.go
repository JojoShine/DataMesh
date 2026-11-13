package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGovernanceStreamReconcileCompletedPause(t *testing.T) {
	id := strings.Repeat("a", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/jobs/overview":
			w.Write([]byte(`{"jobs":[{"jid":"` + id + `","name":"` + streamJobName(id, 1) + `","state":"FINISHED"}]}`))
		case "/jobs/" + id:
			w.Write([]byte(`{"jid":"` + id + `","state":"FINISHED"}`))
		case "/jobs/" + id + "/savepoints/" + id:
			w.Write([]byte(`{"status":{"id":"IN_PROGRESS"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("GOVERNANCE_FLINK_CLUSTER", server.URL)
	s := &Server{runtime: &Runtime{client: server.Client()}}
	row := GovernanceStream{ID: id, Generation: 1, State: "unknown", DesiredState: "paused", SubmissionStarted: true, JobID: id, StopTrigger: id, Sources: encode(map[string]governanceStreamSource{})}
	if e := s.streamStep(context.Background(), &row, governanceFlowExecution{}); e != nil {
		t.Fatal(e)
	}
	if row.State != "pausing" {
		t.Fatalf("lost resumable savepoint after uncertain stop response: %s", row.State)
	}
}

func TestGovernanceStreamCanceledWorkReleasesOnlyOwnedLease(t *testing.T) {
	for _, tc := range []struct {
		name, state, want        string
		cancelled, expired, lost bool
	}{
		{name: "running", state: "running", want: "running"},
		{name: "worker-shutdown", state: "running", want: "unknown", cancelled: true},
		{name: "runtime-timeout", state: "pausing", want: "unknown", expired: true},
		{name: "confirmed-stop", state: "failed", want: "failed", cancelled: true},
		{name: "confirmed-savepoint", state: "paused", want: "paused", cancelled: true},
		{name: "ownership-changed", state: "running", want: "unknown", cancelled: true, lost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=unused dbname=unused"}), &gorm.Config{DisableAutomaticPing: true, DryRun: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			ctx := context.Background()
			if tc.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if tc.expired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			called := false
			err = db.Callback().Update().After("gorm:update").Register("verify_control_finalization", func(tx *gorm.DB) {
				called = true
				if tx.Statement.Context.Err() != nil {
					t.Error("expired runtime context reached lease persistence")
				}
				if tc.cancelled || tc.expired {
					deadline, ok := tx.Statement.Context.Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
						t.Error("control save must have a fresh bounded context")
					}
				}
				values := tx.Statement.Dest.(map[string]interface{})
				if values["state"] != tc.want || values["lease_owner"] != "" || !values["lease_expires_at"].(time.Time).Equal(time.Unix(0, 0).UTC()) {
					t.Errorf("control state/lease not finalized: %v", values)
				}
				if _, changed := values["desired_state"]; changed && tc.want != "failed" {
					t.Error("uncertain work overwrote a concurrent stop request")
				}
				query := tx.Statement.SQL.String()
				for _, fence := range []string{"id=", "generation=", "lease_owner=", "lease_expires_at>CURRENT_TIMESTAMP"} {
					if !strings.Contains(query, fence) {
						t.Errorf("missing ownership fence %s", fence)
					}
				}
				if !tc.lost {
					tx.RowsAffected = 1
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			err = (&Server{db: db}).streamFinish(ctx, GovernanceStream{ID: strings.Repeat("a", 32), Generation: 1, LeaseOwner: "original-owner", State: tc.state, DesiredState: "running", SubmissionStarted: true})
			if !called {
				t.Fatal("control save never attempted")
			}
			if tc.lost {
				failure, ok := err.(*Problem)
				if !ok || failure.Code != "STREAM_LEASE_LOST" {
					t.Fatalf("stale owner overwrite not fenced: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
