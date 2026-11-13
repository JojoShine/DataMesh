package workflow

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestQualityFormatChecks(t *testing.T) {
	cases := []struct{ kind, good, bad string }{{"mobile", "13800138000", "123456"}, {"email", "reader@example.org", "reader@"}, {"date", "2026-11-09", "2026/11/09"}, {"integer", "-0012", "1.2"}, {"decimal", "123456789012345.67", "12e3"}, {"no_whitespace", "客户001", "客户 001"}}
	field := Field{Name: "value", LogicalType: "string", Nullable: true}
	for _, v := range cases {
		c := Check{Kind: "format", Field: "value", Parameters: map[string]json.RawMessage{"format": encode(v.kind)}, Blocking: true}
		if e := validateRule(RuleInput{Name: "format", SchemaVersion: 1, Checks: []Check{c}}, Object{SchemaVersion: 1, Fields: []Field{field}}); e != nil {
			t.Fatal(e)
		}
		re := regexp.MustCompile(qualityFormatPattern(c))
		if !re.MatchString(v.good) || re.MatchString(v.bad) || re.MatchString("") {
			t.Fatalf("format %s", v.kind)
		}
		q := &governanceSQL{}
		if sql := q.quality(c, field); !strings.Contains(sql, " ~ ") || strings.Contains(sql, v.good) {
			t.Fatal("unbound format predicate")
		}
		d := &governanceDorisSQL{}
		if !strings.Contains(d.quality(c, field), "REGEXP(") {
			t.Fatal("missing real Doris predicate")
		}
	}
	invalidCheck := Check{Kind: "format", Field: "value", Parameters: map[string]json.RawMessage{"format": encode("arbitrary")}}
	if validateRule(RuleInput{Name: "format", SchemaVersion: 1, Checks: []Check{invalidCheck}}, Object{SchemaVersion: 1, Fields: []Field{field}}) == nil {
		t.Fatal("unknown format accepted")
	}
}
