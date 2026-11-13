package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
	"gorm.io/gorm"
)

type resourceBenchmark struct {
	Rows                 int
	GovernedValidationMS int64
	Results              []resourceBenchmarkResult
}
type resourceBenchmarkResult struct {
	runID                string
	Kind                 string               `json:"kind"`
	Trial                string               `json:"trial"`
	Rows                 int                  `json:"rows"`
	LogicalBytes         int64                `json:"logical_bytes"`
	ExecutionMS          int64                `json:"execution_ms"`
	ThroughReportMS      int64                `json:"through_report_ms"`
	PostCheckMS          int64                `json:"post_check_ms"`
	GovernedValidationMS int64                `json:"governed_validation_ms,omitempty"`
	PeakHeapBytes        uint64               `json:"peak_heap_bytes"`
	Timings              *workflow.ETLTimings `json:"timings"`
	CheckedRows          int64                `json:"checked_rows"`
	FullValuesVerified   bool                 `json:"full_values_verified"`
	ReceiptAccepted      bool                 `json:"receipt_accepted"`
}
type benchmarkMemory struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
	peak uint64
}

func startBenchmarkMemory() *benchmarkMemory {
	m := &benchmarkMemory{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			if stats.HeapAlloc > m.peak {
				m.peak = stats.HeapAlloc
			}
			select {
			case <-m.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return m
}
func (m *benchmarkMemory) finish() uint64 {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	return m.peak
}
func (b *resourceBenchmark) collect(t *testing.T, db *gorm.DB, router http.Handler, cookie *http.Cookie, csrf, taskID, runID, schema, mode, trial string, started time.Time, run workflow.ETLRun, memory *benchmarkMemory) {
	t.Helper()
	defer memory.finish()
	execution := time.Since(started).Milliseconds()
	var page struct {
		Latest *workflow.ETLReconciliation `json:"latest"`
	}
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		w := workflowRequest(router, "GET", "/api/v1/etl-runs/"+runID+"/reconciliation", nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("million report HTTP %d", w.Code)
		}
		if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if page.Latest != nil && (page.Latest.State == "passed" || page.Latest.State == "failed" || page.Latest.State == "mismatched") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if page.Latest == nil || page.Latest.State != "passed" || page.Latest.CheckedRows != int64(b.Rows) || page.Latest.KeyCount != int64(b.Rows) || page.Latest.MismatchCount != 0 {
		t.Fatalf("million full reconciliation failed: %+v", page.Latest)
	}
	through := time.Since(started).Milliseconds()
	peak := memory.finish()
	// Compare every expected key and value. A count alone would miss substitutions.
	end := b.Rows
	condition := ""
	if mode != "normal" {
		end++
		condition = " WHERE i<>2"
	}
	var missing, bad int64
	q := "SELECT count(*) FROM (SELECT i FROM generate_series(1,?) i" + condition + ") expected LEFT JOIN " + schema + ".target t ON t.id=expected.i WHERE t.id IS NULL OR t.name IS DISTINCT FROM '***'"
	if e := db.Raw(q, end).Scan(&missing).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Raw("SELECT count(*) FROM "+schema+".target WHERE id<1 OR id>? OR name IS DISTINCT FROM '***'", end).Scan(&bad).Error; e != nil {
		t.Fatal(e)
	}
	if missing != 0 || bad != 0 {
		t.Fatalf("million values differ: missing=%d unexpected=%d", missing, bad)
	}
	var columns int64
	if e := db.Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema=? AND table_name='target'", schema).Scan(&columns).Error; e != nil || columns != 2 {
		t.Fatal("unauthorized field appeared in target")
	}
	w := workflowRequest(router, "POST", "/api/v1/etl-runs/"+runID+"/receipts", jsonBody(t, map[string]interface{}{"decision": "accepted", "reason": "全量核验通过", "expected_version": 1, "reconciliation_id": page.Latest.ID}), cookie, csrf, "million-receipt-"+runID)
	if w.Code != 201 {
		t.Fatalf("million receipt HTTP %d %s", w.Code, w.Body.String())
	}
	if run.Timings == nil || run.Timings.TotalMS <= 0 {
		t.Fatal("million stage timing missing")
	}
	timings := *run.Timings
	result := resourceBenchmarkResult{runID: runID, Kind: mode, Trial: trial, Rows: b.Rows, LogicalBytes: run.Bytes, ExecutionMS: execution, ThroughReportMS: through, PostCheckMS: page.Latest.DurationMS, PeakHeapBytes: peak, Timings: &timings, CheckedRows: page.Latest.CheckedRows, FullValuesVerified: true, ReceiptAccepted: true}
	if mode == "governed_snapshot" {
		result.GovernedValidationMS = b.GovernedValidationMS
		if result.GovernedValidationMS <= 0 {
			t.Fatal("governed validation duration missing")
		}
	}
	b.Results = append(b.Results, result)
	output, e := json.MarshalIndent(b.Results, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("ETL_RESOURCE_BENCHMARK_RESULT"), append(output, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("BENCHMARK %s %s rows=%d execution_ms=%d through_report_ms=%d peak_heap=%d", mode, trial, b.Rows, execution, through, peak)
}
func TestETLResourceMillionRows(t *testing.T) {
	if os.Getenv("ETL_RESOURCE_BENCHMARK") != "1" {
		t.Skip("explicit isolated benchmark runner required")
	}
	testResourceBenchmark(t, 1000000)
}
func TestETLResourceBenchmarkPilot(t *testing.T) {
	if os.Getenv("ETL_RESOURCE_BENCHMARK") != "pilot" {
		t.Skip("explicit isolated pilot required")
	}
	testResourceBenchmark(t, 10001)
}
func testResourceBenchmark(t *testing.T, rows int) {
	if os.Getenv("ETL_RESOURCE_BENCHMARK_RESULT") == "" || os.Getenv("RUSTFS_ACCESS_KEY") == "" || os.Getenv("RUSTFS_SECRET_KEY") == "" {
		t.Fatal("benchmark fixtures are missing")
	}
	b := &resourceBenchmark{Rows: rows}
	for _, mode := range []string{"normal", "raw_snapshot", "governed_snapshot"} {
		if !t.Run(mode, func(t *testing.T) { testETLResourceBulk(t, mode, b) }) {
			break
		}
	}
	if len(b.Results) != 6 {
		t.Fatalf("incomplete benchmark: %d/6", len(b.Results))
	}
	db, _, _ := fixture(t, false)
	for _, result := range b.Results {
		var record workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", result.runID).Take(&record).Error; e != nil {
			t.Fatal(e)
		}
		var run workflow.ETLRun
		if e := json.Unmarshal(record.Body, &run); e != nil {
			t.Fatal(e)
		}
		if run.Timings == nil || result.Timings == nil || *run.Timings != *result.Timings {
			t.Fatalf("%s %s stage timings differ from durable run", result.Kind, result.Trial)
		}
	}
}
