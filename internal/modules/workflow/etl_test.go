package workflow

import (
	"strings"
	"testing"
)

func TestETLRules(t *testing.T) {
	for _, tc := range []struct {
		rule  string
		input interface{}
		want  interface{}
	}{
		{"trim", "  hello  ", "hello"}, {"upper", []byte("aBc"), "ABC"}, {"lower", "AB", "ab"}, {"copy", int64(12), int64(12)}, {"trim", nil, nil},
	} {
		got, err := transformETL(tc.input, tc.rule)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %v %v", tc.rule, got, err)
		}
	}
	if _, err := transformETL(12, "trim"); err == nil {
		t.Fatal("numeric trim accepted")
	}
	if _, err := transformETL("value", "sql"); err == nil {
		t.Fatal("arbitrary rule accepted")
	}
}
func TestETLMappingValidation(t *testing.T) {
	source := TableConfig{Keys: []string{"id"}, Fields: []Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string"}}}
	target := TableConfig{Fields: source.Fields, Keys: []string{"id"}}
	good := []ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}
	if err := validateETLMapping(good, source, target); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]ETLMapping{nil, {{Source: "id", Target: "id", Rule: "trim"}}, {{Source: "missing", Target: "id", Rule: "copy"}}, {{Source: "name", Target: "name", Rule: "copy"}}, append(good, good[0])} {
		if err := validateETLMapping(bad, source, target); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	target.Keys = nil
	if err := validateETLMapping(good, source, target); err == nil {
		t.Fatal("target without key accepted")
	}
}
func TestETLInsertQuotesAndBinds(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql"} {
		q := etlInsert(kind, TableConfig{Schema: "public", Table: "target", Keys: []string{"id"}}, []ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "odd\"`column", Rule: "copy"}})
		if strings.Contains(q, "DELETE") || !strings.Contains(q, "INSERT INTO") {
			t.Fatal(q)
		}
		if kind == "postgres" && !strings.Contains(q, "$2") {
			t.Fatal(q)
		}
		if kind == "mysql" && strings.Count(q, "?") != 2 {
			t.Fatal(q)
		}
	}
}

func TestETLLifecycleTransitions(t *testing.T) {
	for _, tc := range []struct{ state, action, want string }{{"draft", "enable", "ready"}, {"ready", "disable", "disabled"}, {"disabled", "enable", "ready"}, {"disabled", "archive", "archived"}, {"draft", "archive", "archived"}} {
		got, err := etlTaskTransition(tc.state, tc.action)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%s: %s %v", tc.state, tc.action, got, err)
		}
	}
	for _, tc := range [][2]string{{"archived", "enable"}, {"ready", "archive"}, {"draft", "disable"}} {
		if _, err := etlTaskTransition(tc[0], tc[1]); err == nil {
			t.Fatalf("accepted %v", tc)
		}
	}
}

func TestETLBatchInsert(t *testing.T) {
	mapping := []ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}
	target := TableConfig{Schema: "public", Table: "target", Keys: []string{"id"}}
	pg := etlBatchInsert("postgres", target, mapping, 3)
	if !strings.Contains(pg, "($1,$2),($3,$4),($5,$6)") || !strings.Contains(pg, `"name"=EXCLUDED."name"`) {
		t.Fatal(pg)
	}
	my := etlBatchInsert("mysql", target, mapping, 3)
	if strings.Count(my, "?") != 6 || !strings.Contains(my, "ON DUPLICATE KEY UPDATE") {
		t.Fatal(my)
	}
	if etlBatchInsert("postgres", target, mapping, 0) != "" {
		t.Fatal("empty batch accepted")
	}
}

func TestETLDecimalPrecision(t *testing.T) {
	p, s := 20, 6
	small := 2
	from := TableConfig{Keys: []string{"amount"}, Fields: []Field{{Name: "amount", LogicalType: "decimal", Precision: &p, Scale: &s}}}
	to := TableConfig{Keys: []string{"amount"}, Fields: []Field{{Name: "amount", LogicalType: "decimal", Precision: &p, Scale: &small}}}
	if e := validateETLMapping([]ETLMapping{{Source: "amount", Target: "amount", Rule: "copy"}}, from, to); e == nil {
		t.Fatal("lossy decimal narrowing accepted")
	}
}
func TestETLTargetAliases(t *testing.T) {
	d := TableConfig{Schema: "public", Table: "customers"}
	a := SourceSecret{Kind: "postgres", Host: "db.example", Port: 5432, Database: "business"}
	b := a
	b.Host = "192.0.2.1"
	b.Port = 15432
	if etlTargetFingerprint(a, d) != etlTargetFingerprint(b, d) {
		t.Fatal("host aliases bypass unresolved target reservation")
	}
}
