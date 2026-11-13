package workflow

import (
	"bytes"
	"encoding/json"
	"github.com/xuri/excelize/v2"
	"testing"
)

func TestSecurityImplicitTraceBinding(t *testing.T) {
	p := SecurityDeliveryProof{ID: "0123456789abcdef0123456789abcdef", ActorID: "alice", KeyID: "key-1", Resource: json.RawMessage(`{"resource_id":"sample","contract_version":1}`)}
	key := bytes.Repeat([]byte{42}, 32)
	token := securityTraceToken(p, key)
	if id, err := securityTraceID(token); err != nil || id != p.ID {
		t.Fatal("trace roundtrip failed", err)
	}
	changed := p
	changed.ActorID = "bob"
	if securityTraceToken(changed, key) == token {
		t.Fatal("actor not bound")
	}
	changed = p
	changed.Resource = json.RawMessage(`{"resource_id":"other","contract_version":1}`)
	if securityTraceToken(changed, key) == token {
		t.Fatal("resource not bound")
	}
	if securityTraceToken(p, bytes.Repeat([]byte{1}, 32)) == token {
		t.Fatal("key not bound")
	}
	body, err := securityTraceJSON([]byte(`{"rows":[{"id":1}]}`), token)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || string(object["rows"]) != "[{\"id\":1}]" {
		t.Fatal("payload changed")
	}
	for _, raw := range []string{`null`, `[]`, `{"_datamesh_trace":"spoof"}`} {
		if _, err = securityTraceJSON([]byte(raw), token); err == nil {
			t.Fatal("accepted unsupported/ambiguous payload")
		}
	}
}
func TestSecurityImplicitExcelRoundtrip(t *testing.T) {
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetCellStr("Sheet1", "A1", "教育部门"); err != nil {
		t.Fatal(err)
	}
	body, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	p := SecurityDeliveryProof{ID: "0123456789abcdef0123456789abcdef", ActorID: "alice", KeyID: "key-1", Resource: json.RawMessage(`{}`)}
	token := securityTraceToken(p, bytes.Repeat([]byte{42}, 32))
	marked, err := securityTraceExcel(body.Bytes(), token)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := securityExtractExcelTrace(marked)
	if err != nil || recovered != token {
		t.Fatal("trace lost", err)
	}
	opened, err := excelize.OpenReader(bytes.NewReader(marked))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	cell, err := opened.GetCellValue("Sheet1", "A1")
	if err != nil || cell != "教育部门" {
		t.Fatal("visible content changed", cell, err)
	}
	if _, err = securityTraceExcel(marked, token); err == nil {
		t.Fatal("duplicate trace accepted")
	}
	if _, err = securityExtractExcelTrace(body.Bytes()); err == nil {
		t.Fatal("unmarked file accepted")
	}
}
