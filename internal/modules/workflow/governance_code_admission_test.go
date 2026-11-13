package workflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"datamesh/internal/platform/coderunner"
)

func admissionFixture(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("private-test-token-with-32-bytes!!"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_RUNNER_URL", server.URL)
	t.Setenv("CODE_RUNNER_TOKEN_FILE", token)
}
func TestCodeRunnerWaitsForRejectedAdmission(t *testing.T) {
	var calls atomic.Int32
	admissionFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/jobs" {
			t.Error("unexpected route")
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":"SCRIPT_CAPACITY"}}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"job_id":"created-once"}`))
	})
	var result struct {
		JobID string `json:"job_id"`
	}
	if err := createCodeRunnerJob(context.Background(), coderunner.CreateJob{Language: "python"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.JobID != "created-once" || calls.Load() != 3 {
		t.Fatal("admission did not complete exactly once")
	}
}
func TestCodeRunnerDoesNotRetryUncertainOrOtherFailures(t *testing.T) {
	for _, status := range []int{400, 409, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			admissionFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"SCRIPT_CAPACITY"}}`))
			})
			if createCodeRunnerJob(context.Background(), coderunner.CreateJob{}, nil) == nil || calls.Load() != 1 {
				t.Fatal("non-admission failure was replayed")
			}
		})
	}
	t.Run("transport interruption", func(t *testing.T) {
		var calls atomic.Int32
		admissionFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
		})
		if createCodeRunnerJob(context.Background(), coderunner.CreateJob{}, nil) == nil || calls.Load() != 1 {
			t.Fatal("uncertain POST was replayed")
		}
	})
}
func TestCodeRunnerAdmissionHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	admissionFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"SCRIPT_CAPACITY"}}`))
		cancel()
	})
	if err := createCodeRunnerJob(ctx, coderunner.CreateJob{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled admission replayed")
	}
}
