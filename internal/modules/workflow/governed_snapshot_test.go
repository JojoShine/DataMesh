package workflow

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConfiguredGovernedResultNoFallback(t *testing.T) {
	defer func() {
		if recover() != nil {
			t.Fatal("failed explicit binding fell through to the legacy result reader")
		}
	}()
	server := &Server{}
	record := Record{ID: "dataset", Version: 1, Body: json.RawMessage(`{"result_check_id":"explicit-check","quality_status":"blocked"}`)}
	_, _, _, e := server.assess(context.Background(), nil, record)
	if captureErrorCode(e) != "QUALITY_BLOCKED" {
		t.Fatal("failed explicit result binding did not block publication")
	}
}
