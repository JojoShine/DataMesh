package workflow

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestETLDigestCanonicalization(t *testing.T) {
	integer := Field{Name: "id", LogicalType: "integer"}
	decimal := Field{Name: "amount", LogicalType: "decimal"}
	text := Field{Name: "text", LogicalType: "string"}
	a, e := canonicalETLValue(integer, json.Number("9007199254740993"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := canonicalETLValue(integer, "9007199254740993")
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal("large integer lost precision")
	}
	c, _ := canonicalETLValue(integer, "9007199254740992")
	if bytes.Equal(a, c) {
		t.Fatal("large integers collided")
	}
	a, _ = canonicalETLValue(decimal, "1.2300")
	b, _ = canonicalETLValue(decimal, "1.23")
	if !bytes.Equal(a, b) {
		t.Fatal("equivalent decimal scales differ")
	}
	c, _ = canonicalETLValue(decimal, "1.24")
	if bytes.Equal(a, c) {
		t.Fatal("different decimals collided")
	}
	a, _ = canonicalETLValue(text, nil)
	b, _ = canonicalETLValue(text, "")
	if bytes.Equal(a, b) {
		t.Fatal("null equals empty string")
	}
	a, _ = canonicalETLValue(Field{LogicalType: "binary"}, []byte("data"))
	b, _ = canonicalETLValue(text, "data")
	if bytes.Equal(a, b) {
		t.Fatal("binary conflated with text")
	}
	precision := 3
	stamp := Field{LogicalType: "datetime", Precision: &precision}
	a, _ = canonicalETLValue(stamp, "2026-10-21T12:00:00.1234+08:00")
	b, _ = canonicalETLValue(stamp, time.Date(2026, 10, 21, 4, 0, 0, 123000000, time.UTC))
	if !bytes.Equal(a, b) {
		t.Fatal("target precision or timezone differs")
	}
	if _, e = canonicalETLValue(decimal, math.Inf(1)); e == nil {
		t.Fatal("nonfinite decimal accepted")
	}
	if _, e = canonicalETLValue(integer, float64(9007199254740993)); e == nil {
		t.Fatal("unsafe float integer accepted")
	}
}
func TestETLDigestScope(t *testing.T) {
	key := bytes.Repeat([]byte{4}, 32)
	fields := []Field{{Name: "name", LogicalType: "string"}, {Name: "value", LogicalType: "string"}}
	a, e := etlRowDigest(key, "run-a", fields, []interface{}{"a", "bc"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := etlRowDigest(key, "run-a", fields, []interface{}{"ab", "c"})
	if bytes.Equal(a, b) {
		t.Fatal("column boundaries not isolated")
	}
	b, _ = etlRowDigest(key, "run-b", fields, []interface{}{"a", "bc"})
	if bytes.Equal(a, b) {
		t.Fatal("digest reused across runs")
	}
	b, _ = etlRowDigest(bytes.Repeat([]byte{5}, 32), "run-a", fields, []interface{}{"a", "bc"})
	if bytes.Equal(a, b) {
		t.Fatal("unkeyed digest")
	}
}
