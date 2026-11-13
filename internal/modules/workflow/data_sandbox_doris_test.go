package workflow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSandboxDorisRejectsUnsafePhysicalTargets(t *testing.T) {
	for _, name := range []string{"", "datamesh_dwd.result", "t_123", "t_00000000000000000000000000000000;DROP DATABASE datamesh_dwd"} {
		if err := sandboxDorisDrop(context.Background(), name); err == nil {
			t.Fatalf("accepted unsafe physical target %q", name)
		}
	}
}

func TestSandboxDorisActualRestrictedCSVEngine(t *testing.T) {
	if os.Getenv("SANDBOX_DORIS_TEST") != "1" {
		t.Skip("requires sandbox-only Doris credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	table := "t_" + id
	defer func() {
		if err := sandboxDorisDrop(context.Background(), table); err != nil {
			t.Error("owned fixture cleanup failed")
		}
	}()
	precision, scale := 30, 6
	fields := []Field{{Name: "code", LogicalType: "string", Nullable: true}, {Name: "big_value", LogicalType: "integer", Nullable: false}, {Name: "amount", LogicalType: "decimal", Nullable: true, Precision: &precision, Scale: &scale}, {Name: "empty", LogicalType: "string", Nullable: true}}
	values := [][]interface{}{{"0012", "9223372036854775807", "9007199254740993.123456", ""}, {"中文\r\nquoted,\"", "-9223372036854775808", nil, nil}}
	if err = sandboxDorisWrite(ctx, table, fields, values, "sandbox_"+id); err != nil {
		t.Fatal("sandbox load failed", err)
	}
	ok, err := sandboxDorisVerify(ctx, table, fields, values)
	if err != nil || !ok {
		t.Fatal("exact verification failed", err)
	}
	got, err := sandboxDorisRead(ctx, "SELECT code,big_value,amount,empty FROM "+sandboxDatabase+"."+table+" ORDER BY _dm_ordinal", 100, 4<<20)
	if err != nil || len(got.Values) != 2 {
		t.Fatal("sandbox read failed", err)
	}
	if got.Values[0][0] != "0012" || got.Values[0][1] != "9223372036854775807" || got.Values[0][2] != "9007199254740993.123456" || got.Values[0][3] != "" || got.Values[1][2] != nil || got.Values[1][3] != nil {
		t.Fatal("precision, leading zero, empty or NULL changed")
	}
	compiled, err := compileSandboxSQL("WITH picked AS (SELECT code, amount FROM people WHERE code = '0012') SELECT code, SUM(amount) AS total FROM picked GROUP BY code", map[string]sandboxSQLTable{"people": {Database:sandboxDatabase,Table:table,Fields:fields}})
	if err != nil { t.Fatal("query compilation failed",err) }
	queried, err := sandboxDorisRead(ctx,compiled,100,4<<20)
	if err != nil || len(queried.Values)!=1 || queried.Values[0][0]!="0012" || queried.Values[0][1]!="9007199254740993.123456" { t.Fatal("actual compiled CTE/filter/aggregate failed",err) }
	limited, err := sandboxDorisRead(ctx, "SELECT code FROM "+sandboxDatabase+"."+table+" ORDER BY _dm_ordinal", 1, 4<<20)
	if err != nil || !limited.Truncated || len(limited.Values) != 1 {
		t.Fatal("preview did not signal truncation", err)
	}
	if _, err = sandboxDorisRead(ctx, "SELECT * FROM datamesh_smoke.customers LIMIT 0", 1, 4<<20); err == nil {
		t.Fatal("sandbox reader accessed main namespace")
	}
}

func TestSandboxDorisActualWideCSVLimit(t *testing.T) {
	if os.Getenv("SANDBOX_DORIS_TEST") != "1" {
		t.Skip("requires sandbox-only Doris credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	table := "t_" + id
	defer func() {
		if err := sandboxDorisDrop(context.Background(), table); err != nil {
			t.Error("wide fixture cleanup failed")
		}
	}()
	fields := make([]Field, 100)
	row := make([]interface{}, 100)
	for i := range fields {
		fields[i] = Field{Name: fmt.Sprintf("column_%d", i+1), LogicalType: "string", Nullable: true}
		row[i] = ""
	}
	values := make([][]interface{}, lightweightMaxRows)
	for i := range values {
		values[i] = row
	}
	// This is a valid ~10MiB CSV. Its field names must not be repeated per-cell on the wire.
	if err := sandboxDorisWrite(ctx, table, fields, values, "sandbox_"+id); err != nil {
		t.Fatal("valid 100-column/100000-row CSV failed", err)
	}
	ok, err := sandboxDorisVerify(ctx, table, fields, values)
	if err != nil || !ok {
		t.Fatal("wide import failed verification", err)
	}
}
