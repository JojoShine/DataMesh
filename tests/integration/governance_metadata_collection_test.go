package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceMetadataAutomaticMaintenance(t *testing.T) {
	u, e := url.Parse(os.Getenv("TEST_APP_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/")}})))
	db, service, router := fixture(t, false)
	owner, ownerName := businessUser(t, service, "autometadata_admin_", "autometadata", []string{"admin"})
	registrar, _ := businessUser(t, service, "autometadata_exchange_", "autometadata", []string{"provider"})
	governor, governorName := businessUser(t, service, "autometadata_governor_", "autometadata", []string{"governor"})
	_, otherName := businessUser(t, service, "autometadata_other_", "other-autometadata", []string{"governor"})
	cookie, actor := login(t, router, governorName)
	adminCookie, administrator := login(t, router, ownerName)
	otherCookie, _ := login(t, router, otherName)
	pass, _ := u.User.Password()
	input := workflow.SourceInput{Name: "自动维护验证来源", Kind: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), OwnerID: owner, Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), adminCookie, administrator.CSRFToken, "autometadata-source")
	if w.Code != 201 {
		t.Fatalf("source=%d %s", w.Code, w.Body.String())
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &source); e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("CREATE TABLE public.autometadata_customers(customer_id bigint PRIMARY KEY,customer_name varchar(64) DEFAULT 'new'); COMMENT ON COLUMN public.autometadata_customers.customer_name IS '客户姓名'; GRANT SELECT ON public.autometadata_customers TO datamesh_app").Error; e != nil {
		t.Fatal(e)
	}
	objectID := "autometadata-" + source.ID
	original := workflow.Object{ObjectID: objectID, SourceID: source.ID, QualifiedName: "public.autometadata_customers", SchemaVersion: 1, Fields: []workflow.Field{{Name: "customer_id", LogicalType: "integer", PrimaryKey: true}, {Name: "customer_name", LogicalType: "string", Nullable: true}}}
	if e = db.Create(&workflow.Record{Kind: "object", ID: objectID, DepartmentID: "autometadata", OwnerID: registrar, ParentID: source.ID, State: "available", Version: 1, Body: jsonBody(t, original)}).Error; e != nil {
		t.Fatal(e)
	}
	seedAssignment(t, db, governor, "autometadata", "govern_resource")
	worker, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/governance/metadata/" + objectID
	loadPolicy := func() workflow.MetadataCollectionPolicy {
		t.Helper()
		var row workflow.MetadataCollectionPolicy
		if e := db.Where("object_id=?", objectID).Take(&row).Error; e != nil {
			t.Fatal(e)
		}
		return row
	}
	countRuns := func() int64 {
		t.Helper()
		var n int64
		if e := db.Model(&workflow.MetadataCollectionRun{}).Where("object_id=?", objectID).Count(&n).Error; e != nil {
			t.Fatal(e)
		}
		return n
	}
	sweep := func(want int64) {
		t.Helper()
		stop := worker.StartGovernanceMaintenanceWorker(context.Background())
		defer stop()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if countRuns() >= want {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("automatic collection did not finish: want %d", want)
	}
	due := func() {
		t.Helper()
		if e := db.Model(&workflow.MetadataCollectionPolicy{}).Where("object_id=?", objectID).Update("next_at", time.Now().Add(-time.Second)).Error; e != nil {
			t.Fatal(e)
		}
	}
	post := func(suffix string, body interface{}, key string, want int) {
		t.Helper()
		w := workflowRequest(router, "POST", path+suffix, jsonBody(t, body), cookie, actor.CSRFToken, key)
		if w.Code != want {
			t.Fatalf("%s=%d %s", suffix, w.Code, w.Body.String())
		}
	}
	sweep(1)
	policy := loadPolicy()
	if policy.ActorID != governor || policy.IntervalMinutes != 1440 || policy.LastSuccessAt == nil || policy.Failures != 0 {
		t.Fatalf("automatic policy=%+v", policy)
	}
	var first workflow.MetadataObservation
	if e = db.Where("object_id=?", objectID).Take(&first).Error; e != nil {
		t.Fatal(e)
	}
	var columns []workflow.MetadataColumn
	if e = json.Unmarshal(first.Columns, &columns); e != nil {
		t.Fatal(e)
	}
	if len(columns) != 2 || columns[1].Comment != "客户姓名" || columns[1].Default == nil {
		t.Fatalf("source comments/defaults not collected: %+v", columns)
	}
	due()
	sweep(2)
	var snapshotCount int64
	if e = db.Model(&workflow.MetadataObservation{}).Where("object_id=?", objectID).Count(&snapshotCount).Error; e != nil {
		t.Fatal(e)
	}
	if snapshotCount != 1 {
		t.Fatalf("unchanged schema created %d snapshots", snapshotCount)
	}
	if e = db.Exec("ALTER TABLE public.autometadata_customers ADD COLUMN customer_level integer").Error; e != nil {
		t.Fatal(e)
	}
	due()
	sweep(3)
	var second workflow.MetadataObservation
	if e = db.Where("object_id=?", objectID).Order("version DESC").Take(&second).Error; e != nil {
		t.Fatal(e)
	}
	if second.Version != 2 || !strings.Contains(string(second.Changes), "field_added") || second.MatchesActive {
		t.Fatalf("change not observed: %+v", second)
	}
	var unchanged workflow.Record
	if e = db.Where("kind='object' AND id=?", objectID).Take(&unchanged).Error; e != nil {
		t.Fatal(e)
	}
	var contract workflow.Object
	if e = json.Unmarshal(unchanged.Body, &contract); e != nil {
		t.Fatal(e)
	}
	if unchanged.Version != 1 || len(contract.Fields) != 2 {
		t.Fatal("automatic collector modified the exchange contract")
	}
	for _, suffix := range []string{"/collection", "/observations/" + second.ID} {
		if w := workflowRequest(router, "GET", path+suffix, nil, otherCookie, "", ""); w.Code != 403 {
			t.Fatalf("cross-department read %s=%d", suffix, w.Code)
		}
	}
	post("/review", map[string]interface{}{"observation_id": second.ID, "state": "acknowledged", "note": "已核查", "impact_checked": false}, "review-missing-impact", 400)
	post("/review", map[string]interface{}{"observation_id": second.ID, "state": "deferred", "note": "新增客户等级，等待销售部确认含义", "impact_checked": true}, "review-defer", 200)
	post("/review", map[string]interface{}{"observation_id": second.ID, "state": "acknowledged", "note": "原任务未引用新增可空字段，业务解释另行补充", "impact_checked": true}, "review-finish", 200)
	policy = loadPolicy()
	post("/collection", map[string]interface{}{"expected_version": policy.Version, "enabled": false, "interval_minutes": 30}, "pause-metadata", 200)
	before := countRuns()
	stop := worker.StartGovernanceMaintenanceWorker(context.Background())
	time.Sleep(150 * time.Millisecond)
	stop()
	if countRuns() != before {
		t.Fatal("saving a paused policy triggered a collection")
	}
	policy = loadPolicy()
	post("/collect", map[string]int{"expected_version": policy.Version}, "manual-paused", 202)
	sweep(before + 1)
	if loadPolicy().Enabled {
		t.Fatal("manual collection enabled paused schedule")
	}
	stop = worker.StartGovernanceMaintenanceWorker(context.Background())
	time.Sleep(150 * time.Millisecond)
	stop()
	if countRuns() != before+1 {
		t.Fatal("paused one-shot request repeated")
	}
	policy = loadPolicy()
	post("/collection", map[string]interface{}{"expected_version": policy.Version, "enabled": true, "interval_minutes": 30}, "resume-metadata", 200)
	sweep(before + 2)
	if e = db.Model(&workflow.Record{}).Where("kind='source' AND id=?", source.ID).Update("version", 2).Error; e != nil {
		t.Fatal(e)
	}
	sweep(before + 3)
	if loadPolicy().SourceVersion != 2 {
		t.Fatal("source version change did not trigger early refresh")
	}
	if e = db.Model(&workflow.Record{}).Where("kind='assignment' AND department_id='autometadata' AND body->>'user_id'=?", governor).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	due()
	sweep(before + 4)
	policy = loadPolicy()
	if policy.Failures != 1 || policy.ErrorCode != "METADATA_AUTHORIZATION_LOST" {
		t.Fatalf("revoked actor still collected: %+v", policy)
	}
	if e = db.Model(&workflow.Record{}).Where("kind='assignment' AND department_id='autometadata' AND body->>'user_id'=?", governor).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("DROP TABLE public.autometadata_customers").Error; e != nil {
		t.Fatal(e)
	}
	due()
	sweep(before + 5)
	policy = loadPolicy()
	if policy.ErrorCode != "TABLE_NOT_FOUND" || policy.LastSuccessAt == nil {
		t.Fatalf("missing source did not retain failure and prior success: %+v", policy)
	}
	if e = db.Model(&workflow.MetadataObservation{}).Where("object_id=?", objectID).Count(&snapshotCount).Error; e != nil {
		t.Fatal(e)
	}
	if snapshotCount != 3 {
		t.Fatalf("failed scan changed snapshots: %d", snapshotCount)
	}
	post("/collection", map[string]interface{}{"expected_version": policy.Version, "enabled": true, "interval_minutes": 14}, "interval-limit", 400)
	// Metadata and data-processing limits are independent; no business rows are read here.
	names := []string{}
	for i := 0; i < 101; i++ {
		names = append(names, fmt.Sprintf("f%d text", i))
	}
	if e = db.Exec("CREATE TABLE public.autometadata_wide(" + strings.Join(names, ",") + "); GRANT SELECT ON public.autometadata_wide TO datamesh_app").Error; e != nil {
		t.Fatal(e)
	}
	wide := original
	wide.ObjectID = objectID + "-wide"
	wide.QualifiedName = "public.autometadata_wide"
	wide.Fields = []workflow.Field{}
	if e = db.Create(&workflow.Record{Kind: "object", ID: wide.ObjectID, DepartmentID: "autometadata", OwnerID: registrar, ParentID: source.ID, State: "available", Version: 1, Body: jsonBody(t, wide)}).Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/metadata/"+wide.ObjectID+"/refresh", jsonBody(t, map[string]int{"source_version": 2, "object_version": 1}), cookie, actor.CSRFToken, "wide-metadata")
	if w.Code != 201 {
		t.Fatalf("metadata 101-field source=%d %s", w.Code, w.Body.String())
	}
}
