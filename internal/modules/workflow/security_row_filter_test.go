package workflow

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/sijms/go-ora/v3/parameter_coder"
	oraTypes "github.com/sijms/go-ora/v3/types"
)

func TestSecurityRowFilterSQLBoundValues(t *testing.T) {
	fields := []Field{{Name: "tenant", LogicalType: "string"}, {Name: "amount", LogicalType: "decimal"}}
	filters := []SecurityRowFilter{{Field: "tenant", Operator: "eq", Value: json.RawMessage(`"x' OR 1=1 --"`)}, {Field: "amount", Operator: "gte", Value: json.RawMessage(`9007199254740993.01`)}}
	sql, args, err := securityRowFilterSQL("postgres", filters, fields, 2)
	if err != nil || sql != `"tenant" = $3 AND "amount" >= $4` || len(args) != 2 || args[0] != "x' OR 1=1 --" || args[1] != "9007199254740993.01" {
		t.Fatalf("SQL=%s args=%v err=%v", sql, args, err)
	}
	if strings.Contains(sql, "OR 1=1") {
		t.Fatal("interpolated value")
	}
}

func TestSecurityRowFilterQuotedNumericLiterals(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal"}}
	filters := []SecurityRowFilter{
		{Field: "id", Operator: "eq", Value: json.RawMessage(`"9007199254740993"`)},
		{Field: "amount", Operator: "in", Value: json.RawMessage(`["9007199254740993.01234567890123456789",0.1,"1e-28"]`)},
	}
	for _, kind := range []string{"postgres", "mysql", "oracle"} {
		_, args, err := securityRowFilterSQL(kind, filters, fields, 0)
		if kind == "oracle" && err == nil {
			for i, arg := range args {
				number, ok := arg.(oraTypes.Number)
				if !ok {
					t.Fatalf("Oracle parameter %d is %T instead of NUMBER", i, arg)
				}
				text, err := number.String()
				if err != nil {
					t.Fatal(err)
				}
				args[i] = text
			}
		}
		exponentValue := "1e-28"
		if kind == "mysql" || kind == "oracle" {
			exponentValue = "0." + strings.Repeat("0", 27) + "1"
		}
		if err != nil || len(args) != 4 || args[0] != "9007199254740993" || args[1] != "9007199254740993.01234567890123456789" || args[2] != "0.1" || args[3] != exponentValue {
			t.Fatalf("%s: args=%v err=%v", kind, args, err)
		}
	}
	for _, raw := range []string{`"1 OR 1=1"`, `" 1"`, `"1 "`, `"NaN"`, `"1e999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999"`, `""`, `"1\u0000"`} {
		if _, err := securityRowFilterValue(json.RawMessage(raw), "decimal"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := securityRowFilterValue(json.RawMessage(`"1.5"`), "integer"); err == nil {
		t.Fatal("fractional quoted integer accepted")
	}
}

func TestSecurityOracleNumericParametersAreExactNumbers(t *testing.T) {
	for _, literal := range []string{"9007199254740993", "9007199254740993.01234567890123456789", "-9007199254740993.01", "1E-28", "1e-128", "-1e-130", "9.9999999999999999999999999999999999999e125", "0.00001", "-0.000e125"} {
		number, err := securityOracleNumericValue(literal)
		if err != nil {
			t.Fatalf("%s: %v", literal, err)
		}
		var parameter parameter_coder.NumberParameter
		if err = parameter.Encode(number, nil); err != nil {
			t.Fatal(err)
		}
		if parameter.GetParameterInfo().DataType != oraTypes.NUMBER {
			t.Fatal("numeric predicate not bound as native NUMBER")
		}
		decoded, err := parameter.Decode(nil)
		if err != nil {
			t.Fatal(err)
		}
		text, ok := decoded.(string)
		if !ok {
			t.Fatalf("unexpected decoded number %T", decoded)
		}
		want, valid := new(big.Rat).SetString(literal)
		got, parsed := new(big.Rat).SetString(text)
		if !valid || !parsed || want.Cmp(got) != 0 {
			t.Fatalf("NUMBER roundtrip changed %s to %s", literal, text)
		}
	}
	for _, literal := range []string{strings.Repeat("9", 39), "1e126", "-1e126", "1e-131", "1e-130", "1e-129", "1e2147483648", "NaN"} {
		if _, err := securityOracleNumericValue(literal); err == nil {
			t.Fatalf("accepted rounded/out-of-range Oracle number %s", literal)
		}
	}
	fields := []Field{{Name: "id", LogicalType: "integer"}}
	q, args, err := securityRowFilterSQL("oracle", []SecurityRowFilter{{Field: "id", Operator: "eq", Value: json.RawMessage(`"9007199254740993"`)}}, fields, 2)
	if err != nil || q != `"id" = :3` || len(args) != 1 {
		t.Fatalf("SQL=%s args=%v err=%v", q, args, err)
	}
	if _, ok := args[0].(oraTypes.Number); !ok {
		t.Fatal("Oracle numeric bind remained a string")
	}
}

type securityOracleDateConnection struct {
	parameter_coder.IConnection
	asUTC bool
}

func (c securityOracleDateConnection) SendTimeAsUTC() bool               { return c.asUTC }
func (securityOracleDateConnection) GetDBTimeZone() *time.Location       { return time.UTC }
func (securityOracleDateConnection) GetDBServerTimeZone() *time.Location { return time.UTC }

func TestSecurityOracleDateFiltersUseNativeCalendarValues(t *testing.T) {
	fields := []Field{{Name: "day", LogicalType: "date"}}
	for _, literal := range []string{"0001-01-01", "2024-02-29", "2026-11-11", "9999-12-31"} {
		q, args, err := securityRowFilterSQL("oracle", []SecurityRowFilter{{Field: "day", Operator: "eq", Value: encode(literal)}}, fields, 1)
		if err != nil || q != `"day" = :2` || len(args) != 1 {
			t.Fatalf("SQL=%s args=%v err=%v", q, args, err)
		}
		date, ok := args[0].(oraTypes.Date)
		if !ok || date.GetDataType() != oraTypes.DATE {
			t.Fatal("date-only predicate did not bind native DATE")
		}
		for _, asUTC := range []bool{false, true} {
			connection := securityOracleDateConnection{asUTC: asUTC}
			var parameter parameter_coder.DateParameter
			if err = parameter.Encode(date, connection); err != nil {
				t.Fatal(err)
			}
			if parameter.GetParameterInfo().DataType != oraTypes.DATE {
				t.Fatal("DATE converted to another bind type")
			}
			decoded, err := parameter.Decode(connection)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := decoded.(time.Time)
			if !ok || got.Format("2006-01-02") != literal || got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 || got.Nanosecond() != 0 {
				t.Fatalf("DATE roundtrip changed %s to %v", literal, decoded)
			}
		}
	}
	for _, literal := range []string{"0000-01-01", "2026-02-29", "2026-11-11T00:00:00Z"} {
		if _, err := securityOracleDateValue(literal); err == nil {
			t.Fatalf("invalid native DATE accepted: %s", literal)
		}
	}
}

func TestSecurityOracleDatetimeFiltersFailClosedWithoutTimezoneContract(t *testing.T) {
	fields := []Field{{Name: "stamp", LogicalType: "datetime"}}
	for _, literal := range []string{"2026-11-11T12:00:00Z", "2026-11-11T20:00:00+08:00", "2026-11-11T12:00:00.123456789Z"} {
		for _, operator := range []string{"eq", "lt", "in"} {
			value := encode(literal)
			if operator == "in" {
				value = encode([]string{literal})
			}
			q, args, err := securityRowFilterSQL("oracle", []SecurityRowFilter{{Field: "stamp", Operator: operator, Value: value}}, fields, 0)
			if err == nil || q != "" || args != nil {
				t.Fatal("ambiguous Oracle datetime predicate reached SQL")
			}
		}
	}
	for _, operator := range []string{"is_null", "not_null"} {
		if _, _, err := securityRowFilterSQL("oracle", []SecurityRowFilter{{Field: "stamp", Operator: operator}}, fields, 0); err != nil {
			t.Fatal("NULL-only temporal predicate requires no conversion", err)
		}
	}
}

func TestSecurityMySQLNumericPredicatesUseExactDecimal(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal"}}
	filters := []SecurityRowFilter{{Field: "id", Operator: "eq", Value: json.RawMessage(`"9007199254740993"`)}, {Field: "amount", Operator: "in", Value: json.RawMessage(`["9007199254740993.01",1.2300e-2,"-1e2"]`)}}
	q, args, err := securityRowFilterSQL("mysql", filters, fields, 0)
	want := "`id` = CAST(? AS DECIMAL(16,0)) AND `amount` IN (CAST(? AS DECIMAL(18,2)),CAST(? AS DECIMAL(4,4)),CAST(? AS DECIMAL(3,0)))"
	if err != nil || q != want || len(args) != 4 || args[0] != "9007199254740993" || args[1] != "9007199254740993.01" || args[2] != "0.0123" || args[3] != "-100" {
		t.Fatalf("SQL=%s args=%v err=%v", q, args, err)
	}
	for _, literal := range []string{"1e65", "1e-31", strings.Repeat("9", 66), "1e2147483648"} {
		filter := SecurityRowFilter{Field: "amount", Operator: "eq", Value: encode(literal)}
		if _, _, err := securityRowFilterSQL("mysql", []SecurityRowFilter{filter}, fields, 0); err == nil {
			t.Fatalf("accepted lossy value %s", literal)
		}
	}
	for _, literal := range []string{"1e64", "1e-30", "-0.000e12345", "1.0000000000000000000000000000000"} {
		if _, _, _, err := securityMySQLNumericLiteral(literal); err != nil {
			t.Fatalf("rejected exact value %s: %v", literal, err)
		}
	}
}
func TestSecurityRowFiltersRejectInvalidDefinitions(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "flag", LogicalType: "boolean"}, {Name: "day", LogicalType: "date"}}
	for _, raw := range []string{`{"field":"missing","operator":"eq","value":1}`, `{"field":"id","operator":"eq","value":"1 OR 1=1"}`, `{"field":"id","operator":"eq","value":1.5}`, `{"field":"id","operator":"eq","value":null}`, `{"field":"id","operator":"in","value":[]}`, `{"field":"id","operator":"OR 1=1","value":1}`, `{"field":"flag","operator":"lt","value":true}`, `{"field":"day","operator":"eq","value":"2026-99-99"}`, `{"field":"id","operator":"is_null","value":1}`} {
		var f SecurityRowFilter
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatal(err)
		}
		if _, _, err := securityRowFilterSQL("postgres", []SecurityRowFilter{f}, fields, 0); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestSecurityRowFilterNullAndMembership(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "other", LogicalType: "string"}}
	filters := []SecurityRowFilter{{Field: "id", Operator: "in", Value: json.RawMessage(`[1,2]`)}, {Field: "other", Operator: "not_null"}}
	for _, kind := range []string{"postgres", "mysql", "oracle"} {
		q, args, err := securityRowFilterSQL(kind, filters, fields, 0)
		if err != nil || len(args) != 2 || !strings.Contains(q, " IS NOT NULL") {
			t.Fatalf("%s: %s %v %v", kind, q, args, err)
		}
	}
}

func TestSecurityFilterPolicyAndInvisibleValidation(t *testing.T) {
	ref := Resource{Type: "table_resource", ID: "table", Version: 1}
	d := SecurityDefinition{Name: "filter", Resource: &ref, Actions: []string{"query"}, RowFilters: []SecurityRowFilter{{Field: "id", Operator: "eq", Value: json.RawMessage(`1`)}}}
	if err := validateSecurityDefinition("policy", d); err != nil {
		t.Fatal(err)
	}
	d.Resource = nil
	if err := validateSecurityDefinition("policy", d); err == nil {
		t.Fatal("unscoped filter accepted")
	}
	d = SecurityDefinition{Name: "invisible", Resource: &ref, Actions: []string{"query"}, Invisible: true}
	if err := validateSecurityDefinition("policy", d); err == nil {
		t.Fatal("invisible without template accepted")
	}
	d.Template = &GovernanceRef{ID: "template", Version: 1}
	if err := validateSecurityDefinition("policy", d); err != nil {
		t.Fatal(err)
	}
	out := SecurityDecision{}
	if err := securityMergeDecision(&out, SecurityDecision{Invisible: true}); err != nil || !out.Invisible {
		t.Fatal("invisible lost through inheritance", err)
	}
}
