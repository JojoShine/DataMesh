package coderunner

import "encoding/json"

const MaxCodeBytes = 64 * 1024
const MaxBatchBytes = 2 * 1024 * 1024
const MaxRowBytes = 256 * 1024
const MaxRows = 250

type Runtime struct {
	DisplayName string   `json:"display_name"`
	Language    string   `json:"language"`
	Version     string   `json:"runtime_version"`
	Image       string   `json:"-"`
	Libraries   []string `json:"libraries"`
	Available   bool     `json:"available"`
}
type CreateJob struct {
	Language       string                     `json:"language"`
	RuntimeVersion string                     `json:"runtime_version"`
	Source         string                     `json:"source"`
	Context        map[string]json.RawMessage `json:"context"`
	TimeoutMS      int                        `json:"timeout_ms"`
}
type Row struct {
	Ordinal int64                      `json:"ordinal"`
	Values  map[string]json.RawMessage `json:"values"`
}
type Batch struct {
	Sequence int   `json:"sequence"`
	Rows     []Row `json:"rows"`
}
type ScriptError struct {
	Code   string `json:"code"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}
type Reply struct {
	Type     string       `json:"type,omitempty"`
	Sequence int          `json:"sequence"`
	Rows     []Row        `json:"rows,omitempty"`
	Error    *ScriptError `json:"error,omitempty"`
}
