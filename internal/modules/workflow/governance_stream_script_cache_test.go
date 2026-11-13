package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"datamesh/internal/platform/coderunner"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type streamMemoS3 struct {
	mu            sync.Mutex
	objects       map[string][]byte
	bucket        bool
	forbidden     bool
	puts, creates int
	putBodies     [][]byte
}

func (f *streamMemoS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Error><Code>%s</Code><Message>private upstream detail</Message></Error>", code)
	}
	if f.forbidden {
		fail(403, "AccessDenied")
		return
	}
	if r.URL.Path == "/datamesh-governance-cache/" || r.URL.Path == "/datamesh-governance-cache" {
		if r.Method != http.MethodPut {
			fail(400, "InvalidRequest")
			return
		}
		f.creates++
		if f.bucket {
			fail(409, "BucketAlreadyOwnedByYou")
			return
		}
		f.bucket = true
		w.WriteHeader(200)
		return
	}
	if !f.bucket {
		fail(404, "NoSuchBucket")
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, exists := f.objects[r.URL.Path]
		if !exists {
			fail(404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Last-Modified", "Wed, 11 Nov 2026 01:00:00 GMT")
		w.Write(body)
	case http.MethodPut:
		f.puts++
		if r.Header.Get("If-None-Match") != "*" {
			fail(400, "InvalidRequest")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail(400, "IncompleteBody")
			return
		}
		f.putBodies = append(f.putBodies, append([]byte(nil), body...))
		if _, exists := f.objects[r.URL.Path]; exists {
			fail(412, "PreconditionFailed")
			return
		}
		f.objects[r.URL.Path] = body
		w.Header().Set("ETag", `"test-etag"`)
		w.WriteHeader(200)
	default:
		fail(400, "InvalidRequest")
	}
}

func streamMemoTestServer(t *testing.T, f http.Handler, transport http.RoundTripper) *Server {
	t.Helper()
	upstream := httptest.NewServer(f)
	t.Cleanup(upstream.Close)
	if transport == nil {
		transport = http.DefaultTransport
	}
	client, err := minio.New(strings.TrimPrefix(upstream.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("", "", ""), Secure: false, Region: "us-east-1", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{runtime: &Runtime{store: client, key: bytes.Repeat([]byte{7}, 32)}}
}

func TestStreamScriptMemoAtomicWinnerAndRestart(t *testing.T) {
	f := &streamMemoS3{objects: map[string][]byte{}}
	s := streamMemoTestServer(t, f, nil)
	ready, release := make(chan struct{}, 2), make(chan struct{})
	rows, errs := make(chan DataRow, 2), make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(value int) {
			row, err := s.streamScriptMemo(context.Background(), "stream/node/row/input", func() (DataRow, error) {
				ready <- struct{}{}
				<-release
				return DataRow{"value": json.RawMessage(fmt.Sprint(value))}, nil
			})
			rows <- row
			errs <- err
		}(i)
	}
	<-ready
	<-ready
	close(release)
	first, second := <-rows, <-rows
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if string(first["value"]) != string(second["value"]) {
		t.Fatalf("different durable winners: %v %v", first, second)
	}
	restarted := &Server{runtime: &Runtime{store: s.runtime.store, key: append([]byte(nil), s.runtime.key...)}}
	row, err := restarted.streamScriptMemo(context.Background(), "stream/node/row/input", func() (DataRow, error) {
		t.Error("recomputed immutable hit")
		return nil, errors.New("unexpected compute")
	})
	if err != nil || string(row["value"]) != string(first["value"]) {
		t.Fatalf("restart lost winner: %v %v", row, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objects) != 1 || f.puts != 2 || f.creates < 1 {
		t.Fatalf("unexpected writes: %d/%d/%d", len(f.objects), f.puts, f.creates)
	}
	for _, ciphertext := range f.objects {
		if json.Valid(ciphertext) {
			t.Fatal("memo is plaintext")
		}
	}
}

type streamMemoLostResponse struct {
	lost   atomic.Bool
	always bool
}

func (r *streamMemoLostResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		// Real transport failures wait for their write loop before returning.
		// This fake turns success into failure, so isolate its asynchronous
		// writer from the original body that the SDK may seek for a retry.
		body, err := io.ReadAll(req.Body)
		closeErr := req.Body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	response, err := http.DefaultTransport.RoundTrip(req)
	if err == nil && req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/stream/") && (r.always || r.lost.CompareAndSwap(false, true)) {
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, errors.New("unacknowledged private transport failure")
	}
	return response, err
}

func TestStreamScriptMemoLostResponseResolvesOnlyThroughImmutableWinner(t *testing.T) {
	for _, unresolved := range []bool{false, true} {
		t.Run(fmt.Sprintf("unresolved=%v", unresolved), func(t *testing.T) {
			f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
			s := streamMemoTestServer(t, f, &streamMemoLostResponse{always: unresolved})
			calls := 0
			compute := func() (DataRow, error) {
				calls++
				return DataRow{"value": json.RawMessage(`9007199254740993`), "text": json.RawMessage(`"中文"`), "optional": json.RawMessage(`null`)}, nil
			}
			first, err := s.streamScriptMemo(context.Background(), "stream/key", compute)
			if !unresolved && (err != nil || string(first["value"]) != "9007199254740993") {
				t.Fatalf("resolved durable winner lost: %v %v", first, err)
			}
			if unresolved {
				if failure, ok := err.(*Problem); !ok || failure.Status != 503 {
					t.Fatalf("unknown write must be unavailable: %v", err)
				}
			}
			row, err := s.streamScriptMemo(context.Background(), "stream/key", compute)
			if err != nil || calls != 1 || string(row["value"]) != "9007199254740993" || string(row["text"]) != `"中文"` || string(row["optional"]) != "null" {
				t.Fatalf("lost memo: %#v %v calls=%d", row, err, calls)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.objects) != 1 || len(f.putBodies) != f.puts || f.puts < 2 {
				t.Fatalf("unexpected conditional writes: %d/%d", len(f.objects), f.puts)
			}
			for _, body := range f.putBodies {
				if !bytes.Equal(body, f.objects["/datamesh-governance-cache/stream/key"]) {
					t.Fatal("conditional retry changed encrypted candidate")
				}
			}
		})
	}
}

func TestStreamScriptMemoRealConnectionLossPreservesWinner(t *testing.T) {
	f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
	var dropped atomic.Bool
	s := streamMemoTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded := httptest.NewRecorder()
		f.ServeHTTP(recorded, r)
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stream/") && dropped.CompareAndSwap(false, true) {
			// Commit first, then lose the actual connection before headers flush.
			// This exercises net/http's real error path without a fake transport.
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		for name, values := range recorded.Header() {
			w.Header()[name] = append([]string(nil), values...)
		}
		w.WriteHeader(recorded.Code)
		w.Write(recorded.Body.Bytes())
	}), nil)
	calls := 0
	row, err := s.streamScriptMemo(context.Background(), "stream/key", func() (DataRow, error) {
		calls++
		return DataRow{"value": json.RawMessage(`42`)}, nil
	})
	if err != nil || string(row["value"]) != "42" || calls != 1 || !dropped.Load() {
		t.Fatalf("connection loss changed winner: row=%v calls=%d error=%v", row, calls, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.putBodies) < 2 || len(f.objects) != 1 {
		t.Fatal("connection loss did not exercise a conditional retry")
	}
	for _, body := range f.putBodies {
		if !bytes.Equal(body, f.objects["/datamesh-governance-cache/stream/key"]) {
			t.Fatal("connection loss changed the encrypted candidate")
		}
	}
}

func TestStreamScriptMemoFailsClosedOnStoreErrorsAndTampering(t *testing.T) {
	f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
	s := streamMemoTestServer(t, f, nil)
	compute := func() (DataRow, error) { return DataRow{"secret": json.RawMessage(`"sensitive"`)}, nil }
	if _, err := s.streamScriptMemo(context.Background(), "stream/key", compute); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects["/datamesh-governance-cache/stream/other"] = append([]byte(nil), f.objects["/datamesh-governance-cache/stream/key"]...)
	f.mu.Unlock()
	for _, key := range []string{"stream/other", "stream/key"} {
		if key == "stream/key" {
			f.mu.Lock()
			f.objects["/datamesh-governance-cache/stream/key"][15] ^= 1
			f.mu.Unlock()
		}
		if _, err := s.streamScriptMemo(context.Background(), key, func() (DataRow, error) { t.Error("tampering triggered compute"); return nil, nil }); err == nil {
			t.Fatal("tampered or wrong AAD memo accepted")
		}
	}
	f.mu.Lock()
	f.forbidden = true
	f.mu.Unlock()
	if _, err := s.streamScriptMemo(context.Background(), "absent", func() (DataRow, error) { t.Error("403 triggered compute"); return nil, nil }); err == nil || strings.Contains(err.Error(), "private upstream detail") {
		t.Fatalf("store failure leaked or succeeded: %v", err)
	}
}

func TestStreamScriptMemoMissingEncryptionKeyDoesNotExecute(t *testing.T) {
	f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
	s := streamMemoTestServer(t, f, nil)
	s.runtime.key = nil
	called := false
	_, err := s.streamScriptMemo(context.Background(), "stream/key", func() (DataRow, error) { called = true; return DataRow{"value": json.RawMessage(`1`)}, nil })
	if err == nil || called {
		t.Fatalf("unavailable encryption executed code: called=%v error=%v", called, err)
	}
}

func TestStreamScriptMemoRejectsInvalidAndOversizedStoredRows(t *testing.T) {
	f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
	s := streamMemoTestServer(t, f, nil)
	for _, plain := range [][]byte{[]byte(`null`), []byte(`[]`), []byte(`{`), append(append([]byte(`{"value":"`), bytes.Repeat([]byte{'a'}, coderunner.MaxRowBytes)...), []byte(`"}`)...)} {
		ciphertext, err := s.runtime.cryptScope(plain, true, "datamesh-stream-script:stream/key")
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.objects["/datamesh-governance-cache/stream/key"] = ciphertext
		f.mu.Unlock()
		_, err = s.streamScriptMemo(context.Background(), "stream/key", func() (DataRow, error) { t.Error("invalid cache triggered compute"); return nil, nil })
		if err == nil {
			t.Fatal("invalid memo accepted")
		}
	}
}

func TestStreamScriptMemoDoesNotPersistFailedExecution(t *testing.T) {
	f := &streamMemoS3{bucket: true, objects: map[string][]byte{}}
	s := streamMemoTestServer(t, f, nil)
	failure := errors.New("bounded script failed")
	_, err := s.streamScriptMemo(context.Background(), "stream/key", func() (DataRow, error) { return nil, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("lost execution error: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 0 || len(f.objects) != 0 {
		t.Fatal("failed execution persisted a memo")
	}
}
