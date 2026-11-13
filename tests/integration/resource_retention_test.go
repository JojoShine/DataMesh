package integration

import (
	"context"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestResourceStorageRetention(t *testing.T) {
	db, service, _ := fixture(t, false)
	uid, _ := businessUser(t, service, "retention_", "retention", []string{"admin"})
	archiveID := "retention-archive-" + uid
	old := time.Now().Add(-48 * time.Hour)
	records := []workflow.Record{
		{Kind: "archive", ID: archiveID, OwnerID: uid, DepartmentID: "retention", State: "verified", Version: 1, Body: []byte(`{}`)},
		{Kind: "etl_run", ID: "unknown-" + uid, OwnerID: uid, DepartmentID: "retention", State: "unknown", Version: 1, Body: []byte(`{}`), UpdatedAt: old},
		{Kind: "etl_run", ID: "failed-" + uid, OwnerID: uid, DepartmentID: "retention", State: "failed", Version: 1, Body: []byte(`{}`), UpdatedAt: old},
	}
	if e := db.Create(&records).Error; e != nil {
		t.Fatal(e)
	}
	for i, state := range []string{"unknown", "failed", "leased"} {
		id := "snapshot-" + state + "-" + uid
		lease := old
		if state == "leased" {
			lease = time.Now().Add(time.Minute)
		}
		snap := workflow.ResourceSnapshot{ID: id, ArchiveID: archiveID, ObjectID: "retention-object", ObjectVersion: i + 1, State: "ready", LeaseExpiresAt: lease, CreatedAt: old, UpdatedAt: old}
		if e := db.Create(&snap).Error; e != nil {
			t.Fatal(e)
		}
		if state != "leased" {
			if e := db.Exec("INSERT INTO resource_snapshot_refs(snapshot_id,run_id) VALUES(?,?)", id, state+"-"+uid).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	server, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stop := server.StartResourceRetentionWorker(context.Background())
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	var remaining int64
	for time.Now().Before(deadline) {
		if e = db.Model(&workflow.ResourceSnapshot{}).Where("archive_id=?", archiveID).Count(&remaining).Error; e != nil {
			t.Fatal(e)
		}
		if remaining == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if remaining != 2 {
		t.Fatalf("expired terminal snapshot not reclaimed: %d", remaining)
	}
	var protected int64
	if e = db.Model(&workflow.ResourceSnapshot{}).Where("id IN ?", []string{"snapshot-unknown-" + uid, "snapshot-leased-" + uid}).Count(&protected).Error; e != nil || protected != 2 {
		t.Fatal("unknown run or live lease lost its immutable input")
	}
}
