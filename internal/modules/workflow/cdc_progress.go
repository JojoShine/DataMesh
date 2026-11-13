package workflow

import (
	"encoding/json"
	"strconv"
	"time"
)

type CDCPartitionRange struct {
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
}
type CDCSnapshotBoundary struct {
	State         string              `json:"state"`
	ObjectVersion int                 `json:"object_version"`
	Partitions    []CDCPartitionRange `json:"partitions"`
}
type CDCSnapshotProgress struct {
	TaskID             string          `json:"task_id" gorm:"primaryKey"`
	State              string          `json:"state"`
	ExpectedRows       int64           `json:"expected_rows"`
	ObservedRows       int64           `json:"observed_rows"`
	Completed          bool            `json:"completed"`
	ErrorCode          string          `json:"error_code,omitempty"`
	NotificationOffset int64           `json:"-"`
	Partitions         json.RawMessage `json:"partitions" gorm:"type:jsonb"`
	LeaseOwner         string          `json:"-"`
	LeaseExpiresAt     time.Time       `json:"-"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

func (CDCSnapshotProgress) TableName() string { return "cdc_snapshot_progress" }

func applyCDCNotification(p *CDCSnapshotProgress, b CDCBinding, raw []byte) error {
	var n struct {
		Aggregate string            `json:"aggregate_type"`
		Type      string            `json:"type"`
		Data      map[string]string `json:"additional_data"`
	}
	payload, _, e := capturePayload(raw)
	if e != nil {
		return e
	}
	if e := json.Unmarshal(payload, &n); e != nil {
		return e
	}
	if n.Aggregate != "Initial Snapshot" || n.Data["connector_name"] != b.TopicPrefix {
		return nil
	}
	switch n.Type {
	case "TABLE_SCAN_COMPLETED":
		collection := n.Data["scanned_collection"]
		if collection != b.Schema+"."+b.Table && collection != b.PDB+"."+b.Schema+"."+b.Table {
			return nil
		}
		rows, e := strconv.ParseInt(n.Data["total_rows_scanned"], 10, 64)
		if e != nil || rows < 0 || n.Data["status"] != "SUCCEEDED" {
			p.ErrorCode = "CDC_SNAPSHOT_UNVERIFIED"
			return nil
		}
		p.ExpectedRows = rows
	case "COMPLETED":
		p.Completed = true
	case "ABORTED", "SKIPPED":
		p.ErrorCode = "CDC_SNAPSHOT_INCOMPLETE"
	}
	p.State = cdcSnapshotState(*p)
	return nil
}
func cdcSnapshotState(p CDCSnapshotProgress) string {
	if p.ErrorCode != "" || p.ExpectedRows >= 0 && p.ObservedRows > p.ExpectedRows {
		return "manual_required"
	}
	if p.Completed && p.ExpectedRows >= 0 && p.ExpectedRows == p.ObservedRows {
		return "complete"
	}
	return "pending"
}
