package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGovernanceStreamSourceEventTime(t *testing.T) {
	for _, raw := range []string{
		`{"op":"u","ts_ms":1793145600000,"source":{"ts_ms":1793059200000}}`,
		`{"schema":null,"payload":{"op":"d","source":{"ts_ms":1793059200000}}}`,
	} {
		stamp := streamSourceEventTime([]byte(raw))
		if stamp == nil || stamp.UnixMilli() != 1793059200000 {
			t.Fatalf("source event timestamp lost: %v", stamp)
		}
	}
	for _, raw := range []string{
		`{"op":"u","ts_ms":1793145600000}`, `{"op":"r","source":{"ts_ms":1793059200000}}`,
		`{"op":"u","source":{"ts_ms":0}}`, `{"op":"u","source":{"ts_ms":-1}}`, `null`, `invalid`, `{"op":"u","source":{"ts_ms":9223372036854775807}}`,
	} {
		if stamp := streamSourceEventTime([]byte(raw)); stamp != nil {
			t.Fatalf("invented source event time for %s: %v", raw, stamp)
		}
	}
}

func TestGovernanceStreamFreshnessUnknownIsNull(t *testing.T) {
	row := GovernanceStream{UpdatedAt: time.Now(), CaughtUp: true, Freshness: emptyStreamFreshness()}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Freshness struct {
			Processed   *time.Time `json:"processed_to_at"`
			Latency     *int64     `json:"end_to_end_latency_ms"`
			DorisOffset *int64     `json:"doris_output_offset"`
		} `json:"freshness"`
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value.Freshness.Processed != nil || value.Freshness.Latency != nil || value.Freshness.DorisOffset != nil {
		t.Fatalf("poll/caught_up invented data freshness: %s", raw)
	}
}

func TestGovernanceStreamForwardingBacklog(t *testing.T) {
	for _, tc := range []struct {
		start, end, cursor int64
		want               *int64
	}{
		{3, 10, 5, streamInt64(5)}, {3, 10, 10, streamInt64(0)}, {3, 10, 2, nil}, {3, 10, 11, nil},
	} {
		got := streamForwardingBacklog(CDCPartitionRange{Start: tc.start, End: tc.end}, tc.cursor)
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Fatalf("invalid backlog for %+v: %v", tc, got)
		}
	}
}

func TestGovernanceStreamComputeHealthUsesLiveJob(t *testing.T) {
	for _, tc := range []struct{ state, want string }{{"RUNNING", "running"}, {"FAILED", "stopped"}, {"FINISHED", "paused"}, {"RESTARTING", "unknown"}} {
		t.Run(tc.state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"state":"` + tc.state + `"}`))
			}))
			defer server.Close()
			t.Setenv("GOVERNANCE_FLINK_CLUSTER", server.URL)
			s := &Server{runtime: &Runtime{client: server.Client()}}
			row := GovernanceStream{State: "paused", JobID: strings.Repeat("a", 32), UpdatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
			out := s.observeStreamCompute(context.Background(), row)
			if out.FlinkCompute.State != tc.want || out.FlinkCompute.ObservedAt == nil || !out.FlinkCompute.ObservedAt.After(row.UpdatedAt) {
				t.Fatalf("incorrect live health: %+v", out.FlinkCompute)
			}
			if out.ProcessedToAt != nil || out.EndToEndLatencyMS != nil {
				t.Fatal("job state inferred data freshness")
			}
		})
	}
}
