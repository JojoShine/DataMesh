package workflow

import "testing"

func TestCDCProgressBoundaries(t *testing.T) {
	p := CDCSnapshotProgress{TaskID: "task", State: "pending", ExpectedRows: -1}
	b := CDCBinding{ConnectorName: "connect-management-name", TopicPrefix: "capture-task", Schema: "public", Table: "orders"}
	if e := applyCDCNotification(&p, b, []byte(`{"aggregate_type":"Initial Snapshot","type":"COMPLETED","additional_data":{"connector_name":"other"}}`)); e != nil {
		t.Fatal(e)
	}
	if p.Completed {
		t.Fatal("another connector completed this snapshot")
	}
	if e := applyCDCNotification(&p, b, []byte(`{"aggregate_type":"Initial Snapshot","type":"TABLE_SCAN_COMPLETED","additional_data":{"connector_name":"capture-task","scanned_collection":"public.orders","total_rows_scanned":"2","status":"SUCCEEDED"}}`)); e != nil {
		t.Fatal(e)
	}
	if e := applyCDCNotification(&p, b, []byte(`{"aggregate_type":"Initial Snapshot","type":"COMPLETED","additional_data":{"connector_name":"capture-task"}}`)); e != nil {
		t.Fatal(e)
	}
	if cdcSnapshotState(p) == "complete" {
		t.Fatal("completion notification alone accepted before data arrives")
	}
	p.ObservedRows = 2
	if cdcSnapshotState(p) != "complete" {
		t.Fatal("verified snapshot not complete")
	}
	p.ObservedRows = 3
	if cdcSnapshotState(p) != "manual_required" {
		t.Fatal("duplicate snapshot attempt silently accepted")
	}
	p.ExpectedRows = 0
	p.ObservedRows = 0
	if cdcSnapshotState(p) != "complete" {
		t.Fatal("empty table completion lost")
	}
	p.ErrorCode = "CDC_RANGE_EXPIRED"
	if cdcSnapshotState(p) == "complete" {
		t.Fatal("expired data accepted")
	}
}
