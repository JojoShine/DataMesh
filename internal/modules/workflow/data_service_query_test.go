package workflow

import "testing"

func TestDataServiceSQLBoundary(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal"}, {Name: "secret", LogicalType: "string", Sensitive: true}}
	q, params, e := compileServiceSelect("SELECT id, secret FROM result WHERE id >= :min AND amount < :max ORDER BY id DESC", fields)
	if e != nil || len(params) != 2 || len(q.fields) != 2 || q.order != "`id` DESC,`_dm_ordinal` ASC" {
		t.Fatalf("query compilation: %+v %+v %v", q, params, e)
	}
	for _, sql := range []string{"SELECT id FROM other", "SELECT id FROM result; DROP TABLE result", "SELECT id FROM result -- comment", "SELECT id FROM result WHERE id = 1 OR 1=1", "SELECT id FROM result WHERE id = :id OR id > :id", "SELECT id FROM result UNION SELECT secret FROM result", "SELECT sleep(id) FROM result", "SELECT id FROM result WHERE secret = :secret", "SELECT id FROM result ORDER BY secret", "SELECT id,id FROM result", "SELECT missing FROM result", "SELECT id FROM result WHERE id=:x AND amount=:x", "SELECT id INTO OUTFILE 'a' FROM result", "SELECT id FROM result WHERE id IN (SELECT id FROM result)", "SELECT id FROM result LIMIT 100000"} {
		t.Run(sql, func(t *testing.T) {
			if _, _, e := compileServiceSelect(sql, fields); e == nil {
				t.Fatal("unsafe or unsupported query accepted")
			}
		})
	}
	for _, sql := range []string{"select id from result", "SELECT id FROM result WHERE id=:id", "SELECT id FROM result ORDER BY id", "SELECT id FROM result WHERE id<>:id ORDER BY id ASC"} {
		if _, _, e := compileServiceSelect(sql, fields); e != nil {
			t.Fatalf("valid query rejected %s: %v", sql, e)
		}
	}
}

func TestDataServiceParameterSemantics(t *testing.T) {
	precision, scale := 18, 2
	for _, tc := range []struct {
		raw   string
		field Field
		want  interface{}
	}{
		{"false", Field{LogicalType: "boolean"}, false},
		{"是", Field{LogicalType: "boolean"}, true},
		{"2026-10-30T08:30:00+08:00", Field{LogicalType: "datetime"}, "2026-10-30 00:30:00"},
		{"2026-10-30", Field{LogicalType: "date"}, "2026-10-30"},
		{"1234567890123456.78", Field{LogicalType: "decimal", Precision: &precision, Scale: &scale}, "1234567890123456.78"},
	} {
		v, e := serviceParameterValue(tc.raw, tc.field)
		if e != nil || v != tc.want {
			t.Fatalf("%s: %v %v", tc.raw, v, e)
		}
	}
	if _, e := serviceParameterValue("1.234", Field{LogicalType: "decimal", Precision: &precision, Scale: &scale}); e == nil {
		t.Fatal("excess scale accepted")
	}
	if _, e := serviceParameterValue("", Field{LogicalType: "integer"}); e == nil {
		t.Fatal("empty required parameter accepted")
	}
}

func TestDataServiceStarAndTerminator(t *testing.T) {
	schema := []Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Sensitive: true}}
	for _, sql := range []string{"SELECT * FROM result", " SELECT * FROM result; ", "SELECT id, name FROM result;"} {
		query, _, err := compileServiceSelect(sql, schema)
		if err != nil || len(query.fields) != 2 || query.fields[0] != "id" || query.fields[1] != "name" {
			t.Fatalf("ordinary SELECT rejected: %q %v", sql, err)
		}
	}
	for _, sql := range []string{"SELECT * FROM result;;", "SELECT * FROM result; SELECT id FROM result", "SELECT * FROM other;", "SELECT * FROM result WHERE name=:name;"} {
		if _, _, err := compileServiceSelect(sql, schema); err == nil {
			t.Fatalf("unsafe SQL accepted: %q", sql)
		}
	}
}
