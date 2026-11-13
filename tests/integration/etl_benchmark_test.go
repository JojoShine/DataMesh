package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
	"datamesh/internal/platform/database"
)

type etlTiming struct {
	Mode             string  `json:"mode"`
	WallSeconds      float64 `json:"wall_seconds"`
	ExecutionSeconds float64 `json:"execution_seconds"`
	RowsPerSecond    float64 `json:"rows_per_second"`
	Read             int     `json:"read"`
	Written          int     `json:"written"`
	Planned          int     `json:"planned"`
	LogicalBytes     int64   `json:"logical_bytes_estimate"`
	Batches          int     `json:"batches"`
	HeapPeakBytes    uint64  `json:"heap_peak_bytes"`
	Verified         bool    `json:"full_content_verified"`
}

func TestETLMillionRows(t *testing.T) {
	if os.Getenv("ETL_BENCHMARK") != "1" {
		t.Skip("explicit ETL_BENCHMARK=1 required for million-row performance run")
	}
	t.Setenv("ETL_MAX_ROWS", "2000000")
	t.Setenv("ETL_MAX_BYTES", "1073741824")
	targetURL, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	sourceURL, e := url.Parse(os.Getenv("ETL_BENCH_SOURCE_DSN"))
	if e != nil || !strings.HasPrefix(sourceURL.Path, "/datamesh_test_") {
		t.Fatal("isolated source database required")
	}
	allowed := []map[string]interface{}{}
	for _, u := range []*url.URL{sourceURL, targetURL} {
		port, _ := strconv.Atoi(u.Port())
		allowed = append(allowed, map[string]interface{}{"kind": "postgres", "host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/")})
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, allowed)))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "million_owner_", "benchmark", []string{"admin"})
	cookie, p := login(t, router, name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source, e := database.Open(ctx, sourceURL.String())
	if e != nil {
		t.Fatal("benchmark source unavailable")
	}
	sourceSQL, e := source.DB()
	if e != nil {
		t.Fatal(e)
	}
	defer sourceSQL.Close()
	create := "CREATE TABLE public.etl_million (id bigint PRIMARY KEY,name varchar(100) NOT NULL,email varchar(160) NOT NULL,amount numeric(20,6) NOT NULL,updated_at timestamp(3) NOT NULL,note varchar(256) NOT NULL)"
	if e = source.Exec(create).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec(create).Error; e != nil {
		t.Fatal(e)
	}
	if e = source.Exec("INSERT INTO public.etl_million SELECT i,' 客户'||i||' ','USER'||i||'@EXAMPLE.TEST',i::numeric/1000000,timestamp '2026-10-20 12:00:00.123',repeat(md5(i::text),4) FROM generate_series(1,1000000) i").Error; e != nil {
		t.Fatal(e)
	}
	if e = source.Exec("ANALYZE public.etl_million").Error; e != nil {
		t.Fatal(e)
	}
	postSetup := func(path string, v interface{}) json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, v), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 201 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	sourceIDs := []string{}
	for _, u := range []*url.URL{sourceURL, targetURL} {
		port, _ := strconv.Atoi(u.Port())
		password, _ := u.User.Password()
		var out struct {
			ID string `json:"source_id"`
		}
		if e = json.Unmarshal(postSetup("/api/v1/sources", workflow.SourceInput{Name: "Million row isolated database", Kind: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), OwnerID: uid, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}), &out); e != nil {
			t.Fatal(e)
		}
		sourceIDs = append(sourceIDs, out.ID)
	}
	mapping := []workflow.ETLMapping{}
	for _, field := range []string{"id", "name", "email", "amount", "updated_at", "note"} {
		rule := "copy"
		if field == "name" {
			rule = "trim"
		}
		if field == "email" {
			rule = "lower"
		}
		mapping = append(mapping, workflow.ETLMapping{Source: field, Target: field, Rule: rule})
	}
	var task struct {
		ID string `json:"id"`
	}
	if e = json.Unmarshal(postSetup("/api/v1/etl-tasks", workflow.ETLDefinition{Name: "Million row exchange", Source: workflow.TableConfig{SourceID: sourceIDs[0], Schema: "public", Table: "etl_million"}, Target: workflow.TableConfig{SourceID: sourceIDs[1], Schema: "public", Table: "etl_million"}, Mapping: mapping}), &task); e != nil {
		t.Fatal(e)
	}
	tls := httptest.NewTLSServer(router)
	defer tls.Close()
	client := tls.Client()
	client.Timeout = 15 * time.Second
	type view struct {
		ID    string          `json:"id"`
		State string          `json:"state"`
		Data  workflow.ETLRun `json:"data"`
	}
	results := []etlTiming{}
	for repetition := 0; repetition < 4; repetition++ {
		mode := fmt.Sprintf("insert_%d", repetition+1)
		if repetition == 3 {
			mode = "upsert_existing"
		} else {
			if e = db.Exec("TRUNCATE public.etl_million").Error; e != nil {
				t.Fatal(e)
			}
		}
		runtime.GC()
		heapPeak := uint64(0)
		started := time.Now()
		req, e := http.NewRequest("POST", tls.URL+"/api/v1/etl-tasks/"+task.ID+"/run", strings.NewReader("{}"))
		if e != nil {
			t.Fatal(e)
		}
		req.AddCookie(cookie)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", p.CSRFToken)
		req.Header.Set("Idempotency-Key", fmt.Sprint(started.UnixNano()))
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil || res.StatusCode != 201 {
			t.Fatalf("benchmark submit %d %s", res.StatusCode, raw)
		}
		var accepted view
		if e = json.Unmarshal(raw, &accepted); e != nil {
			t.Fatal(e)
		}
		var final view
		for time.Since(started) < 10*time.Minute {
			request, e := http.NewRequest("GET", tls.URL+"/api/v1/etl-tasks/"+task.ID+"/runs", nil)
			if e != nil {
				t.Fatal(e)
			}
			request.AddCookie(cookie)
			response, e := client.Do(request)
			if e != nil {
				t.Fatal(e)
			}
			var page struct {
				Items []view `json:"items"`
			}
			e = json.NewDecoder(response.Body).Decode(&page)
			response.Body.Close()
			if e != nil || response.StatusCode != 200 {
				t.Fatal("benchmark progress unavailable")
			}
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			if mem.HeapAlloc > heapPeak {
				heapPeak = mem.HeapAlloc
			}
			for _, v := range page.Items {
				if v.ID == accepted.ID {
					final = v
				}
			}
			if final.State == "succeeded" || final.State == "failed" || final.State == "unknown" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		wall := time.Since(started).Seconds()
		if final.State != "succeeded" || final.Data.Written != 1000000 || final.Data.Read != 1000000 || final.Data.Planned != 1000000 {
			t.Fatalf("million exchange failed: state=%s read=%d written=%d code=%s message=%s", final.State, final.Data.Read, final.Data.Written, final.Data.ErrorCode, final.Data.Message)
		}
		var mismatch, count int64
		if e = db.Raw("SELECT COUNT(*),COUNT(*) FILTER (WHERE name IS DISTINCT FROM '客户'||id OR email IS DISTINCT FROM 'user'||id||'@example.test' OR amount IS DISTINCT FROM (id::numeric/1000000)::numeric(20,6) OR updated_at IS DISTINCT FROM timestamp '2026-10-20 12:00:00.123' OR note IS DISTINCT FROM repeat(md5(id::text),4) OR id<1 OR id>1000000) FROM public.etl_million").Row().Scan(&count, &mismatch); e != nil || count != 1000000 || mismatch != 0 {
			t.Fatalf("full reconciliation failed count=%d mismatch=%d %v", count, mismatch, e)
		}
		execution := final.Data.FinishedAt.Sub(*final.Data.StartedAt).Seconds()
		result := etlTiming{Mode: mode, WallSeconds: wall, ExecutionSeconds: execution, RowsPerSecond: 1000000 / wall, Read: final.Data.Read, Written: final.Data.Written, Planned: final.Data.Planned, LogicalBytes: final.Data.Bytes, Batches: len(final.Data.Batches), HeapPeakBytes: heapPeak, Verified: true}
		results = append(results, result)
		t.Logf("%s: %.3fs wall / %.3fs worker / %.0f rows/s", mode, wall, execution, result.RowsPerSecond)
	}
	var sourceBytes int64
	if e = source.Raw("SELECT pg_total_relation_size('public.etl_million')").Scan(&sourceBytes).Error; e != nil {
		t.Fatal(e)
	}
	var sourceVersion, targetVersion string
	if e = source.Raw("SELECT version()").Scan(&sourceVersion).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Raw("SELECT version()").Scan(&targetVersion).Error; e != nil {
		t.Fatal(e)
	}
	report := map[string]interface{}{"source_version": sourceVersion, "target_version": targetVersion, "measured_at": time.Now().UTC(), "rows": 1000000, "fields": 6, "source": "PostgreSQL separate Docker container", "target": "PostgreSQL metadata container; separate isolated database", "transport": "real HTTPS task submission/polling, application streaming over database network connections", "batch_rows": 500, "gomaxprocs": runtime.GOMAXPROCS(0), "go_version": runtime.Version(), "source_relation_bytes_with_index": sourceBytes, "results": results, "gomemlimit": os.Getenv("GOMEMLIMIT")}
	if status, e := os.ReadFile("/proc/self/status"); e == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				report["process_peak_rss"] = strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
			}
		}
	}
	path := os.Getenv("ETL_BENCHMARK_RESULT")
	if path == "" {
		t.Fatal("benchmark result path required")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	encoded, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(encoded, '\n'), 0644); e != nil {
		t.Fatal(e)
	}
}
