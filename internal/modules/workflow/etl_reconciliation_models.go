package workflow

import (
	"encoding/json"
	"time"
)

type ETLReconciliation struct {
	DurationMS      int64           `json:"duration_ms"`
	ID              string          `json:"id" gorm:"primaryKey"`
	RunID           string          `json:"run_id"`
	Version         int             `json:"version"`
	State           string          `json:"state"`
	InputSnapshotID string          `json:"input_snapshot_id"`
	InputScope      json.RawMessage `json:"input_scope" gorm:"type:jsonb"`
	TaskVersion     int             `json:"task_version"`
	ReadRows        int64           `json:"read_rows"`
	ProcessedRows   int64           `json:"processed_rows"`
	KeyCount        int64           `json:"key_count"`
	CheckedRows     int64           `json:"checked_rows"`
	MismatchCount   int64           `json:"mismatch_count"`
	ErrorCode       string          `json:"error_code,omitempty"`
	CheckedAt       *time.Time      `json:"checked_at"`
	LeaseOwner      string          `json:"-"`
	LeaseExpiresAt  time.Time       `json:"-"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (ETLReconciliation) TableName() string { return "etl_reconciliations" }

type etlDeliveryInput struct {
	RunID          string `gorm:"primaryKey"`
	State          string
	WrappedKey     []byte
	TargetKind     string
	TargetVersion  int
	TargetColumns  json.RawMessage `gorm:"type:jsonb"`
	KeyCount       int64
	StoredBytes    int64
	ReceiptVersion int
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (etlDeliveryInput) TableName() string { return "etl_delivery_inputs" }

type etlDeliveryKey struct {
	RunID          string `gorm:"primaryKey"`
	KeyDigest      []byte `gorm:"primaryKey"`
	EncryptedKey   []byte
	ExpectedDigest []byte
	Sequence       int64
}

func (etlDeliveryKey) TableName() string { return "etl_delivery_keys" }

type etlTargetColumn struct {
	Field           Field  `json:"field"`
	NativeType      string `json:"native_type"`
	Cast            string `json:"cast"`
	KeyCast         string `json:"key_cast"`
	Charset         string `json:"charset"`
	Collation       string `json:"collation"`
	CollationSchema string `json:"collation_schema"`
	Char            bool   `json:"char"`
	MaxLength       int64  `json:"max_length"`
	IntegerBits     int    `json:"integer_bits"`
	Unsigned        bool   `json:"unsigned"`
}
type etlDeliverySession struct {
	Input       etlDeliveryInput
	Key         []byte
	Columns     []etlTargetColumn
	StoredBytes int64
}
