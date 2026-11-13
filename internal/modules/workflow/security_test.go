package workflow

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/xuri/excelize/v2"
)

func TestSecurityProofCanonicalAndBinding(t *testing.T) {
	p := SecurityDeliveryProof{ID: "delivery", ActorID: "recipient", Fields: json.RawMessage(`["id"]`), Resource: json.RawMessage(`{"resource_id":"r","contract_version":1,"resource_type":"raw_object"}`), Controls: json.RawMessage(`[{"id":"policy","version":1}]`), SHA256: "digest", CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	key := bytes.Repeat([]byte{7}, 32)
	signature := securityProofSignature(p, key)
	p.Resource = json.RawMessage(`{ "contract_version": 1, "resource_type":"raw_object", "resource_id":"r" }`)
	p.State = "completed"
	if signature != securityProofSignature(p, key) {
		t.Fatal("JSONB normalization or completion changed immutable signature")
	}
	p.ActorID = "different-recipient"
	if signature == securityProofSignature(p, key) {
		t.Fatal("recipient is not bound")
	}
	p.ActorID = "recipient"
	p.Controls = json.RawMessage(`[{"id":"policy","version":2}]`)
	if signature == securityProofSignature(p, key) {
		t.Fatal("policy version is not bound")
	}
}

func TestSecurityTemplateAndPayload(t *testing.T) {
	for _, text := range []string{"{password}", "{recipient}\nmalformed", "<script>", "{{recipient}}"} {
		if validateSecurityDefinition("watermark", SecurityDefinition{Name: "test", Text: text}) == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	p := identity.Principal{Username: "接收人", DepartmentID: "部门"}
	text := securityRender("{recipient}/{department}/{resource}/{version}/{delivery_id}", p, Resource{ID: "resource", Version: 3}, "proof", time.Now())
	if text != "接收人/部门/resource/3/proof" {
		t.Fatal(text)
	}
	input := []byte(`{"records":[{"secret":"private","id":9007199254740993}],"events":[{"before":{"secret":"private"},"after":{"secret":null},"key":{"secret":"private"}}],"cutoff":{"position":42}}`)
	body, err := securityMaskPayload(input, []string{"secret"})
	if err != nil || bytes.Contains(body, []byte("private")) || !bytes.Contains(body, []byte("9007199254740993")) || !bytes.Contains(body, []byte(`"position":42`)) {
		t.Fatalf("mask changed contract: %s %v", body, err)
	}
}

func TestSecurityExcelDerivedCopy(t *testing.T) {
	book := excelize.NewFile()
	if err := book.SetCellStr("Sheet1", "A1", "9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if err := book.SetCellFormula("Sheet1", "B1", "1+2"); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	book.Close()
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(buffer.Bytes())
	before := sha256.Sum256(original)
	body, err := securityMarkExcel(original, "recipient & department · proof", "proof-id-123456")
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(original) != before || bytes.Equal(body, original) {
		t.Fatal("original changed or derivative missing")
	}
	out, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	value, err := out.GetCellValue("Sheet1", "A1")
	if err != nil || value != "9007199254740993" {
		t.Fatal(value, err)
	}
	formula, err := out.GetCellFormula("Sheet1", "B1")
	if err != nil || formula != "1+2" {
		t.Fatal(formula, err)
	}
	if len(out.GetSheetList()) != 2 {
		t.Fatal("explanation sheet missing")
	}
	header, err := out.GetHeaderFooter("Sheet1")
	if err != nil || header.OddHeader == "" {
		t.Fatal("print header missing", err)
	}
	var signed bytes.Buffer
	writer := zip.NewWriter(&signed)
	entry, err := writer.Create("_xmlsignatures/sig1.xml")
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("signed"))
	writer.Close()
	if _, err = securityMarkExcel(signed.Bytes(), "mark", "proof-id-123456"); err == nil {
		t.Fatal("signed office document accepted")
	}
}
