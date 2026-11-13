package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"datamesh/internal/platform/httpserver"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Keep the database and external engines alive while recreating the application
// lifecycle, so restart assertions exercise durable reconciliation rather than a
// second independent fixture.
type cdcStreamFixture struct {
	db      *gorm.DB
	service *identity.Service
	router  http.Handler
	stop    func()
	started time.Time
}

func newCDCStreamFixture(t *testing.T, db *gorm.DB, service *identity.Service) *cdcStreamFixture {
	t.Helper()
	f := &cdcStreamFixture{db: db, service: service, stop: func() {}, started: time.Now()}
	f.restart(t)
	t.Cleanup(func() { f.stop() })
	return f
}

func (f *cdcStreamFixture) restart(t *testing.T) {
	t.Helper()
	f.phase(t, "stopping application workers")
	f.stop()
	router, stop, err := httpserver.NewWithLifecycle(context.Background(), f.db, f.service, origin)
	if err != nil {
		t.Fatal(err)
	}
	f.router, f.stop = router, stop
	f.phase(t, "application workers ready")
}

func (f *cdcStreamFixture) phase(t *testing.T, stage string) {
	t.Helper()
	db, err := f.db.DB()
	if err != nil {
		t.Fatalf("lifecycle database handle: %T", err)
	}
	stats := db.Stats()
	t.Logf("CDC lifecycle %s elapsed=%s db_open=%d in_use=%d idle=%d max=%d wait_count=%d wait_time=%s", stage, time.Since(f.started).Round(time.Millisecond), stats.OpenConnections, stats.InUse, stats.Idle, stats.MaxOpenConnections, stats.WaitCount, stats.WaitDuration)
}

func cdcStreamDatabaseError(err error) string {
	if err == nil {
		return "none"
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return "postgres:" + pg.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	return fmt.Sprintf("%T", err)
}

// Keep diagnostics scoped to this expensive chain; never print sessions, hashes or credentials.
func (f *cdcStreamFixture) login(t *testing.T, username string) (*http.Cookie, identity.Principal) {
	t.Helper()
	f.phase(t, "login starting")
	started := time.Now()
	w := request(f.router, "POST", "/auth/login", string(jsonBody(t, map[string]string{"username": username, "password": password})), nil, "", origin)
	f.phase(t, "login completed")
	if w.Code != http.StatusOK {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(w.Body.Bytes(), &failure) != nil {
			failure.Code = "INVALID_ERROR_RESPONSE"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var ping int
		pingErr := f.db.WithContext(ctx).Raw("SELECT 1").Scan(&ping).Error
		var exists bool
		lookupErr := f.db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM users WHERE username=?)", username).Scan(&exists).Error
		var activity []struct {
			State         string
			WaitEventType string
			Count         int64
		}
		activityErr := f.db.WithContext(ctx).Raw("SELECT COALESCE(state,'unknown') AS state,COALESCE(wait_event_type,'none') AS wait_event_type,COUNT(*) AS count FROM pg_stat_activity WHERE datname=current_database() GROUP BY state,wait_event_type").Scan(&activity).Error
		t.Fatalf("lifecycle login status=%d code=%s message=%s duration=%s db_ping=%s user_exists=%t lookup=%s activity=%v activity_error=%s", w.Code, failure.Code, failure.Message, time.Since(started).Round(time.Millisecond), cdcStreamDatabaseError(pingErr), exists, cdcStreamDatabaseError(lookupErr), activity, cdcStreamDatabaseError(activityErr))
	}
	var actor identity.Principal
	if err := json.Unmarshal(w.Body.Bytes(), &actor); err != nil {
		t.Fatalf("login principal: %T", err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("login cookie protections missing")
	}
	return cookies[0], actor
}

func cdcStreamAwait(t *testing.T, description string, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type cdcJoinedRow struct {
	ID    string
	Left  string
	Right string
}

func cdcStreamPage(t *testing.T, f *cdcStreamFixture, path string, cookie *http.Cookie) []cdcJoinedRow {
	t.Helper()
	started := time.Now()
	w := workflowRequest(f.router, "GET", path, nil, cookie, "", "")
	if w.Code != http.StatusOK {
		f.phase(t, "records request failed")
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(w.Body.Bytes(), &failure) != nil {
			failure.Code = "INVALID_ERROR_RESPONSE"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var stream workflow.GovernanceStream
		var lookupErr error
		if strings.HasPrefix(path, "/api/v1/governance/streams/") {
			id := strings.Split(strings.TrimPrefix(path, "/api/v1/governance/streams/"), "/")[0]
			lookupErr = f.db.WithContext(ctx).Where("id=?", id).Take(&stream).Error
		}
		t.Fatalf("records status=%d code=%s message=%s elapsed=%s stream_state=%s stream_error=%s desired=%s lease_held=%t lease_remaining=%s stream_lookup=%s", w.Code, failure.Code, failure.Message, time.Since(started).Round(time.Millisecond), stream.State, stream.ErrorCode, stream.DesiredState, stream.LeaseOwner != "", time.Until(stream.LeaseExpiresAt).Round(time.Millisecond), cdcStreamDatabaseError(lookupErr))
	}
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
		Total int64                        `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	rows := make([]cdcJoinedRow, 0, len(page.Items))
	seen := map[string]bool{}
	for _, item := range page.Items {
		text := func(field string) string {
			var value string
			if json.Unmarshal(item[field], &value) == nil {
				return value
			}
			return string(item[field])
		}
		row := cdcJoinedRow{text("id"), text("left_name"), text("right_name")}
		if seen[row.ID] {
			t.Fatalf("duplicate governed key %s", row.ID)
		}
		seen[row.ID] = true
		rows = append(rows, row)
	}
	// All test rows fit on one page; count mismatches expose duplicate/stale rows.
	if int64(len(rows)) != page.Total {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func cdcStreamJobs(t *testing.T, streamID string) map[string]string {
	t.Helper()
	base := os.Getenv("GOVERNANCE_FLINK_CLUSTER")
	if base == "" {
		base = "http://jobmanager:8081"
	}
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(strings.TrimRight(base, "/") + "/jobs/overview")
	if err != nil {
		t.Fatal("actual Flink job inventory unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("actual Flink inventory status=%d", response.StatusCode)
	}
	var inventory struct {
		Jobs []struct {
			ID    string `json:"jid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inventory); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]string{}
	for _, job := range inventory.Jobs {
		if strings.HasPrefix(job.Name, "datamesh-stream-"+streamID+"-") {
			jobs[job.ID] = job.State
		}
	}
	return jobs
}

func cdcStreamTerminal(state string) bool {
	return state == "FAILED" || state == "CANCELED" || state == "FINISHED"
}

func testCDCStreamLifecycle(t *testing.T, f *cdcStreamFixture, source *sql.DB, bindings []workflow.CDCBinding, ownerCookie *http.Cookie, owner identity.Principal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	exec := func(query string) {
		t.Helper()
		if _, err := source.ExecContext(ctx, query); err != nil {
			t.Fatal("isolated live source mutation failed")
		}
	}
	seq := 0
	postAs := func(path string, body interface{}, cookie *http.Cookie, actor identity.Principal, key string) map[string]json.RawMessage {
		t.Helper()
		if key == "" {
			seq++
			key = fmt.Sprintf("cdc-stream-%s-%d", owner.UserID, seq)
		}
		w := workflowRequest(f.router, "POST", path, jsonBody(t, body), cookie, actor.CSRFToken, key)
		if w.Code != 200 && w.Code != 201 && w.Code != 202 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(value["id"], &id); err != nil || id == "" {
			t.Fatal("response missing id")
		}
		return id
	}
	postOwner := func(path string, body interface{}) map[string]json.RawMessage {
		return postAs(path, body, ownerCookie, owner, "")
	}
	f.phase(t, "creating governor account after CDC snapshot/archive")
	governorID, governorName := businessUser(t, f.service, "cdc_stream_governor_", owner.DepartmentID, []string{"governor"})
	governorCookie, governor := f.login(t, governorName)
	seedAssignment(t, f.db, governorID, owner.DepartmentID, "govern_resource")
	edge := func(node string) []workflow.GovernanceFlowEdge { return []workflow.GovernanceFlowEdge{{Node: node}} }
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "left", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "raw_object", ID: bindings[0].ObjectID, Version: 1}, Fields: []string{"id", "name"}}},
		{ID: "right", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "raw_object", ID: bindings[1].ObjectID, Version: 1}, Fields: []string{"id", "name"}}},
		{ID: "joined", Kind: "join", Inputs: []workflow.GovernanceFlowEdge{{Node: "left"}, {Node: "right"}}, Join: &workflow.GovernanceFlowJoin{Kind: "inner", LeftKeys: []string{"id"}, RightKeys: []string{"id"}, Columns: []workflow.GovernanceFlowJoinColumn{{Side: "left", Source: "id", Target: "id"}, {Side: "left", Source: "name", Target: "left_name"}, {Side: "right", Source: "name", Target: "right_name"}}}},
		{ID: "quality", Kind: "quality", Inputs: edge("joined"), Checks: []workflow.Check{{Kind: "required", Field: "left_name", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, InferModel: true, DataLayer: "dwd"},
	}}
	f.phase(t, "publishing governed join")
	flowID := idOf(postOwner("/api/v1/governance/dataflows", map[string]interface{}{"execution_mode": "continuous", "definition": workflow.GovernanceDefinition{Name: "真实CDC持续治理生命周期", OwnerID: owner.UserID, StewardID: governorID, Dataflow: &graph}}))
	postOwner("/api/v1/governance/dataflows/"+flowID+"/versions/1/publish", struct{}{})
	startPath := "/api/v1/governance/dataflows/" + flowID + "/versions/1/stream"
	withoutGrant := workflowRequest(f.router, "POST", startPath, []byte(`{}`), governorCookie, governor.CSRFToken, "ungranted-start-"+flowID)
	if withoutGrant.Code != 403 {
		t.Fatalf("ungranted continuous source=%d %s", withoutGrant.Code, withoutGrant.Body.String())
	}
	grantIDs := []string{}
	for _, binding := range bindings {
		id := "stream-grant-" + binding.TaskID
		grant := workflow.ProcessingAuthorization{Resource: workflow.Resource{Type: "raw_object", ID: binding.ObjectID, Version: 1}, UserID: governorID, Fields: []string{"id", "name"}, Purpose: "数据治理", AllowDerived: true, ExpiresAt: time.Now().Add(time.Hour), ApprovalRef: "isolated-stream-acceptance"}
		if err := f.db.Create(&workflow.Record{Kind: "processing_authorization", ID: id, DepartmentID: owner.DepartmentID, OwnerID: owner.UserID, ParentID: governorID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; err != nil {
			t.Fatal(err)
		}
		grantIDs = append(grantIDs, id)
	}
	startKey := "cdc-stream-start-" + flowID
	streamID := idOf(postAs(startPath, struct{}{}, governorCookie, governor, startKey))
	if repeated := idOf(postAs(startPath, struct{}{}, governorCookie, governor, startKey)); repeated != streamID {
		t.Fatal("repeated start created another stream")
	}
	var streamCount int64
	if err := f.db.Model(&workflow.GovernanceStream{}).Where("flow_id=?", flowID).Count(&streamCount).Error; err != nil || streamCount != 1 {
		t.Fatalf("start count=%d err=%v", streamCount, err)
	}
	streamPath := "/api/v1/governance/streams/" + streamID
	readStream := func() workflow.GovernanceStream {
		t.Helper()
		var row workflow.GovernanceStream
		if err := f.db.Where("id=?", streamID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	awaitState := func(state string) workflow.GovernanceStream {
		t.Helper()
		var row workflow.GovernanceStream
		cdcStreamAwait(t, "stream "+state, 100*time.Second, func() bool {
			row = readStream()
			if row.State == "failed" && state != "failed" {
				t.Fatalf("stream failed while waiting for %s: %s", state, row.ErrorCode)
			}
			return row.State == state
		})
		return row
	}
	// Cleanup deliberately runs before the source connector fixture disappears.
	defer func() {
		w := workflowRequest(f.router, "POST", streamPath+"/stop", []byte(`{}`), ownerCookie, owner.CSRFToken, "cleanup-"+streamID)
		if w.Code != 200 {
			t.Errorf("stream cleanup status=%d %s", w.Code, w.Body.String())
			return
		}
		cdcStreamAwait(t, "confirmed stream termination", 60*time.Second, func() bool {
			if readStream().State != "failed" {
				return false
			}
			for _, state := range cdcStreamJobs(t, streamID) {
				if !cdcStreamTerminal(state) {
					return false
				}
			}
			return true
		})
	}()
	assertOneJob := func(wantID string) {
		t.Helper()
		active := 0
		for id, state := range cdcStreamJobs(t, streamID) {
			if !cdcStreamTerminal(state) {
				active++
				if id != wantID {
					t.Fatalf("unexpected active job %s", id)
				}
			}
		}
		if active != 1 {
			t.Fatalf("active jobs=%d, want one", active)
		}
	}
	awaitRows := func(want []cdcJoinedRow) {
		t.Helper()
		cdcStreamAwait(t, fmt.Sprintf("exact governed rows %v", want), 90*time.Second, func() bool {
			row := readStream()
			if row.State == "failed" {
				t.Fatalf("stream failed: %s", row.ErrorCode)
			}
			return reflect.DeepEqual(cdcStreamPage(t, f, streamPath+"/records", governorCookie), want)
		})
	}
	initial := awaitState("running")
	f.phase(t, "actual Flink job running")
	assertOneJob(initial.JobID)
	awaitRows([]cdcJoinedRow{{"1", "updated", "updated"}, {"3", "created", "created"}})

	// Unlike history replay, these transactions happen after the actual job runs.
	exec("BEGIN; UPDATE cdc_live_a SET name='实时甲' WHERE id=1; UPDATE cdc_live_b SET name='实时乙' WHERE id=1; INSERT INTO cdc_live_a VALUES(4,'新增甲'); INSERT INTO cdc_live_b VALUES(4,'新增乙'); DELETE FROM cdc_live_b WHERE id=3; COMMIT")
	liveRows := []cdcJoinedRow{{"1", "实时甲", "实时乙"}, {"4", "新增甲", "新增乙"}}
	awaitRows(liveRows)
	f.phase(t, "live inserts updates deletes materialized")
	exec("UPDATE cdc_live_a SET name=NULL WHERE id=1")
	awaitRows([]cdcJoinedRow{{"4", "新增甲", "新增乙"}})
	cdcStreamAwait(t, "quality isolation", 20*time.Second, func() bool { return readStream().RejectedRows == 1 })
	exec("UPDATE cdc_live_a SET name='实时甲' WHERE id=1")
	awaitRows(liveRows)
	cdcStreamAwait(t, "quality recovery", 20*time.Second, func() bool { return readStream().RejectedRows == 0 })

	postOwner("/api/v1/tasks/"+bindings[0].TaskID+"/pause", struct{}{})
	cdcStreamAwait(t, "capture pause", 30*time.Second, func() bool {
		var task workflow.Record
		if err := f.db.Where("kind='task' AND id=?", bindings[0].TaskID).Take(&task).Error; err != nil {
			t.Fatal(err)
		}
		return task.State == "paused"
	})
	exec("UPDATE cdc_live_a SET name='采集恢复' WHERE id=1")
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if got := cdcStreamPage(t, f, streamPath+"/records", governorCookie); !reflect.DeepEqual(got, liveRows) {
			t.Fatalf("paused capture advanced: %v", got)
		}
	}
	postOwner("/api/v1/tasks/"+bindings[0].TaskID+"/resume", struct{}{})
	liveRows[0].Left = "采集恢复"
	awaitRows(liveRows)
	var currentBinding workflow.CDCBinding
	if err := f.db.Where("task_id=?", bindings[0].TaskID).Take(&currentBinding).Error; err != nil {
		t.Fatal(err)
	}
	if currentBinding.ConnectorName != bindings[0].ConnectorName || currentBinding.Topic != bindings[0].Topic || currentBinding.Slot != bindings[0].Slot || currentBinding.ConfigVersion != bindings[0].ConfigVersion {
		t.Fatal("capture resume replaced identity/configuration")
	}

	// Browser controls must fail without changing the persisted generation/state.
	beforeDenied := readStream()
	w := workflowRequest(f.router, "POST", streamPath+"/pause", []byte(`{}`), governorCookie, "", "csrf-denied-"+streamID)
	if w.Code != 403 {
		t.Fatalf("missing CSRF=%d", w.Code)
	}
	_, outsiderName := businessUser(t, f.service, "cdc_stream_outsider_", "different-department", []string{"governor"})
	outsiderCookie, outsider := f.login(t, outsiderName)
	for _, action := range []string{"", "/records", "/pause", "/resume", "/snapshot", "/stop"} {
		method, body := "GET", []byte(nil)
		if action != "" && action != "/records" {
			method, body = "POST", []byte(`{}`)
		}
		denied := workflowRequest(f.router, method, streamPath+action, body, outsiderCookie, outsider.CSRFToken, "other-"+action+streamID)
		if denied.Code != 403 && denied.Code != 404 {
			t.Fatalf("cross-scope %s=%d %s", action, denied.Code, denied.Body.String())
		}
	}
	if after := readStream(); after.Generation != beforeDenied.Generation || after.DesiredState != beforeDenied.DesiredState {
		t.Fatal("denied controls changed stream")
	}

	postAs(streamPath+"/pause", struct{}{}, governorCookie, governor, "")
	paused := awaitState("paused")
	f.phase(t, "savepoint paused and drained")
	if paused.Savepoint == "" || !paused.CaughtUp {
		t.Fatal("pause lacks drained recoverable savepoint")
	}
	if !cdcStreamTerminal(cdcStreamJobs(t, streamID)[paused.JobID]) {
		t.Fatal("paused job not terminal")
	}
	pausedRows := cdcStreamPage(t, f, streamPath+"/records", governorCookie)
	snapshotID := idOf(postAs(streamPath+"/snapshot", struct{}{}, governorCookie, governor, ""))
	cdcStreamAwait(t, "immutable governed snapshot", 60*time.Second, func() bool {
		var run workflow.GovernanceRun
		if err := f.db.Where("id=?", snapshotID).Take(&run).Error; err != nil {
			t.Fatal(err)
		}
		if run.State == "failed" {
			t.Fatalf("snapshot failed: %s", run.ErrorCode)
		}
		return run.State == "succeeded"
	})
	snapshotPath := "/api/v1/governance/runs/" + snapshotID + "/records"
	if got := cdcStreamPage(t, f, snapshotPath, governorCookie); !reflect.DeepEqual(got, pausedRows) {
		t.Fatalf("snapshot boundary rows=%v want=%v", got, pausedRows)
	}
	exec("UPDATE cdc_live_a SET name='恢复点之后' WHERE id=1; DELETE FROM cdc_live_a WHERE id=4; INSERT INTO cdc_live_b VALUES(5,'等待关联')")
	resumeKey := "resume-" + streamID
	postAs(streamPath+"/resume", struct{}{}, governorCookie, governor, resumeKey)
	postAs(streamPath+"/resume", struct{}{}, governorCookie, governor, resumeKey)
	resumed := awaitState("running")
	f.phase(t, "savepoint restored")
	if resumed.Generation != paused.Generation+1 || resumed.Savepoint != paused.Savepoint || resumed.JobID == paused.JobID {
		t.Fatal("resume did not restore exactly one generation")
	}
	assertOneJob(resumed.JobID)
	awaitRows([]cdcJoinedRow{{"1", "恢复点之后", "实时乙"}})
	if got := cdcStreamPage(t, f, snapshotPath, governorCookie); !reflect.DeepEqual(got, pausedRows) {
		t.Fatalf("immutable result changed after resume: %v", got)
	}

	var beforeRestart workflow.GovernanceStream
	cdcStreamAwait(t, "in-flight stream worker before restart", 20*time.Second, func() bool {
		beforeRestart = readStream()
		return beforeRestart.LeaseOwner != "" && beforeRestart.LeaseExpiresAt.After(time.Now())
	})
	f.stop()
	if stopped := readStream(); stopped.LeaseOwner != "" && stopped.LeaseExpiresAt.After(time.Now()) {
		t.Fatal("canceled application worker stranded its control lease")
	}
	f.restart(t)
	exec("UPDATE cdc_live_b SET name='应用重启后' WHERE id=1; INSERT INTO cdc_live_a VALUES(5,'恢复关联')")
	awaitRows([]cdcJoinedRow{{"1", "恢复点之后", "应用重启后"}, {"5", "恢复关联", "等待关联"}})
	afterRestart := readStream()
	if afterRestart.Generation != beforeRestart.Generation || afterRestart.JobID != beforeRestart.JobID || afterRestart.OutputOffset < beforeRestart.OutputOffset {
		t.Fatal("worker restart duplicated job or regressed committed boundary")
	}
	assertOneJob(beforeRestart.JobID)
	if got := cdcStreamPage(t, f, snapshotPath, governorCookie); !reflect.DeepEqual(got, pausedRows) {
		t.Fatal("worker restart changed frozen result")
	}

	postOwner("/api/v1/processing-authorizations/"+grantIDs[0]+"/revoke", map[string]interface{}{"expected_version": 1, "reason": "验证真实持续授权撤销"})
	failed := awaitState("failed")
	f.phase(t, "revoked stream terminated")
	if failed.ErrorCode != "PROCESSING_AUTHORIZATION_REQUIRED" && failed.ErrorCode != "GOVERNANCE_AUTHORIZATION_CHANGED" {
		t.Fatalf("revocation error=%s", failed.ErrorCode)
	}
	for _, state := range cdcStreamJobs(t, streamID) {
		if !cdcStreamTerminal(state) {
			t.Fatal("revocation released stream before engine termination")
		}
	}
	denied := workflowRequest(f.router, "GET", streamPath+"/records", nil, governorCookie, "", "")
	if denied.Code != 403 {
		t.Fatalf("revoked live results=%d", denied.Code)
	}
	denied = workflowRequest(f.router, "POST", streamPath+"/resume", []byte(`{}`), governorCookie, governor.CSRFToken, "revoked-resume-"+streamID)
	if denied.Code != 403 && denied.Code != 409 {
		t.Fatalf("revoked resume=%d", denied.Code)
	}
	exec("UPDATE cdc_live_a SET name='撤销后不得交付' WHERE id=1")
	f.restart(t)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		row := readStream()
		if row.State != "failed" || row.OutputOffset != failed.OutputOffset {
			t.Fatal("revoked stream restarted or materialized more data")
		}
	}
}
