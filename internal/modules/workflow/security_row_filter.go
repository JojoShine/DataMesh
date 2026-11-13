package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	oraTypes "github.com/sijms/go-ora/v3/types"
	"gorm.io/gorm"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Every predicate and every applicable policy is combined with AND. Values never
// become SQL text; identifiers must first match the pinned resource contract.
type SecurityRowFilter struct {
	Field    string          `json:"field"`
	Operator string          `json:"operator"`
	Value    json.RawMessage `json:"value,omitempty"`
}

var securityFilterNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
var securityFilterInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func securityValidateRowFilters(filters []SecurityRowFilter, fields []Field) error {
	if len(filters) > 32 {
		return invalid
	}
	for _, f := range filters {
		if !valid(f.Field, 128) || len(f.Value) > 4096 || !slices.Contains([]string{"eq", "ne", "lt", "lte", "gt", "gte", "in", "is_null", "not_null"}, f.Operator) {
			return invalid
		}
		if f.Operator == "is_null" || f.Operator == "not_null" {
			if len(f.Value) > 0 && string(f.Value) != "null" {
				return invalid
			}
		} else if len(f.Value) == 0 || string(f.Value) == "null" || !json.Valid(f.Value) {
			return invalid
		}
		if fields == nil {
			continue
		}
		index := slices.IndexFunc(fields, func(field Field) bool { return field.Name == f.Field })
		if index < 0 {
			return invalid
		}
		typ := fields[index].LogicalType
		if !slices.Contains([]string{"string", "integer", "decimal", "boolean", "date", "datetime"}, typ) {
			return invalid
		}
		if slices.Contains([]string{"lt", "lte", "gt", "gte"}, f.Operator) && !slices.Contains([]string{"integer", "decimal", "date", "datetime"}, typ) {
			return invalid
		}
		if f.Operator == "is_null" || f.Operator == "not_null" {
			continue
		}
		values := []json.RawMessage{f.Value}
		if f.Operator == "in" {
			values = nil
			if json.Unmarshal(f.Value, &values) != nil || len(values) < 1 || len(values) > 100 {
				return invalid
			}
		}
		for _, value := range values {
			if _, err := securityRowFilterValue(value, typ); err != nil {
				return err
			}
		}
	}
	return nil
}
func securityRowFilterValue(raw json.RawMessage, typ string) (interface{}, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, invalid
	}
	switch typ {
	case "integer", "decimal":
		// Browsers cannot represent every valid integer/decimal exactly.
		// Quoted numeric literals preserve their digits through the policy UI.
		if raw[0] == '"' {
			var literal string
			if json.Unmarshal(raw, &literal) != nil {
				return nil, invalid
			}
			raw = []byte(literal)
		}
		pattern := securityFilterNumber
		if typ == "integer" {
			pattern = securityFilterInteger
		}
		if !pattern.Match(raw) || len(raw) > 128 {
			return nil, invalid
		}
		// Keep exact decimals and integers beyond float64's precision.
		return string(raw), nil
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return nil, invalid
		}
		return value, nil
	default:
		var value string
		if json.Unmarshal(raw, &value) != nil || len(value) > 2000 || strings.ContainsRune(value, 0) {
			return nil, invalid
		}
		if typ == "date" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return nil, invalid
			}
		}
		if typ == "datetime" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return nil, invalid
			}
		}
		return value, nil
	}
}
func securityRowFilterSQL(kind string, filters []SecurityRowFilter, fields []Field, offset int) (string, []interface{}, error) {
	if len(filters) == 0 {
		return "", []interface{}{}, nil
	}
	if fields == nil || !slices.Contains([]string{"postgres", "mysql", "oracle"}, kind) || offset < 0 {
		return "", nil, invalid
	}
	if err := securityValidateRowFilters(filters, fields); err != nil {
		return "", nil, err
	}
	args := []interface{}{}
	terms := []string{}
	bind := func(value interface{}) string {
		args = append(args, value)
		switch kind {
		case "postgres":
			return fmt.Sprintf("$%d", offset+len(args))
		case "oracle":
			return fmt.Sprintf(":%d", offset+len(args))
		default:
			return "?"
		}
	}
	for _, f := range filters {
		column := quoteIdentifier(kind, f.Field)
		if f.Operator == "is_null" {
			terms = append(terms, column+" IS NULL")
			continue
		}
		if f.Operator == "not_null" {
			terms = append(terms, column+" IS NOT NULL")
			continue
		}
		field := fields[slices.IndexFunc(fields, func(field Field) bool { return field.Name == f.Field })]
		if kind == "oracle" && field.LogicalType == "datetime" {
			// Oracle DATE, TIMESTAMP and time-zone variants currently share a
			// logical type. Choosing a temporal bind would guess its semantics.
			return "", nil, problem(409, "SECURITY_FILTER_TEMPORAL_UNSUPPORTED", "当前Oracle字段契约未区分日期时间与时区类型，暂不支持此日期时间行过滤")
		}
		values := []json.RawMessage{f.Value}
		if f.Operator == "in" {
			values = nil
			if err := json.Unmarshal(f.Value, &values); err != nil {
				return "", nil, err
			}
		}
		marks := []string{}
		for _, raw := range values {
			value, err := securityRowFilterValue(raw, field.LogicalType)
			if err != nil {
				return "", nil, err
			}
			if kind == "mysql" && (field.LogicalType == "integer" || field.LogicalType == "decimal") {
				literal, ok := value.(string)
				if !ok {
					return "", nil, invalid
				}
				normalized, precision, scale, err := securityMySQLNumericLiteral(literal)
				if err != nil {
					return "", nil, err
				}
				// A VAR_STRING parameter must not turn a numeric comparison into
				// approximate floating-point evaluation. Cast only the parameter,
				// preserving the source column's precision and index semantics.
				marks = append(marks, fmt.Sprintf("CAST(%s AS DECIMAL(%d,%d))", bind(normalized), precision, scale))
			} else if kind == "oracle" && (field.LogicalType == "integer" || field.LogicalType == "decimal") {
				literal, ok := value.(string)
				if !ok {
					return "", nil, invalid
				}
				number, err := securityOracleNumericValue(literal)
				if err != nil {
					return "", nil, err
				}
				marks = append(marks, bind(number))
			} else if kind == "oracle" && field.LogicalType == "date" {
				literal, ok := value.(string)
				if !ok {
					return "", nil, invalid
				}
				date, err := securityOracleDateValue(literal)
				if err != nil {
					return "", nil, err
				}
				marks = append(marks, bind(date))
			} else {
				marks = append(marks, bind(value))
			}
		}
		if f.Operator == "in" {
			terms = append(terms, column+" IN ("+strings.Join(marks, ",")+")")
			continue
		}
		op := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[f.Operator]
		terms = append(terms, column+" "+op+" "+marks[0])
	}
	return strings.Join(terms, " AND "), args, nil
}

func securityOracleDateValue(literal string) (oraTypes.Date, error) {
	parsed, err := time.Parse("2006-01-02", literal)
	if err != nil || parsed.Year() < 1 {
		return oraTypes.Date{}, invalid
	}
	var date oraTypes.Date
	date.SetDataType(oraTypes.DATE)
	if err = date.SetValue(parsed); err != nil {
		return oraTypes.Date{}, err
	}
	return date, nil
}

// Native NUMBER parameters avoid NLS-dependent character conversion. Oracle
// guarantees 38 significant decimal digits and nonzero magnitudes [1e-130,1e126).
func securityOracleNumericValue(literal string) (oraTypes.Number, error) {
	if len(literal) > 128 || !securityFilterNumber.MatchString(literal) {
		return oraTypes.Number{}, invalid
	}
	negative := strings.HasPrefix(literal, "-")
	literal = strings.TrimPrefix(literal, "-")
	exponent := 0
	if index := strings.IndexAny(literal, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(literal[index+1:], 10, 32)
		if err != nil {
			return oraTypes.Number{}, invalid
		}
		exponent = int(parsed)
		literal = literal[:index]
	}
	fraction := 0
	if index := strings.IndexByte(literal, '.'); index >= 0 {
		fraction = len(literal) - index - 1
		literal = literal[:index] + literal[index+1:]
	}
	digits := strings.TrimLeft(literal, "0")
	canonical := "0"
	if digits != "" {
		order := len(digits) - fraction + exponent - 1
		digits = strings.TrimRight(digits, "0")
		if len(digits) > 38 || order < -130 || order > 125 {
			return oraTypes.Number{}, problem(409, "SECURITY_FILTER_PRECISION_UNSUPPORTED", "Oracle行过滤数值超过38位有效数字或NUMBER安全范围")
		}
		canonical = digits + "e" + strconv.Itoa(order-len(digits)+1)
		if negative {
			canonical = "-" + canonical
		}
	}
	number, err := oraTypes.NewNumber(canonical)
	if err != nil {
		return oraTypes.Number{}, err
	}
	// The installed driver mishandles some NUMBER boundary encodings when
	// decoding them. Refuse any value it cannot preserve exactly end to end.
	decoded, err := number.String()
	if err != nil {
		return oraTypes.Number{}, err
	}
	want, valid := new(big.Rat).SetString(canonical)
	got, parsed := new(big.Rat).SetString(decoded)
	if !valid || !parsed || want.Cmp(got) != 0 {
		return oraTypes.Number{}, problem(409, "SECURITY_FILTER_PRECISION_UNSUPPORTED", "Oracle驱动不能精确保留此行过滤数值")
	}
	return *number, nil
}

// Normalize without float64 or database-specific exponent parsing. Choosing
// exactly sufficient precision also preserves ordinary Doris DECIMAL support.
func securityMySQLNumericLiteral(literal string) (string, int, int, error) {
	if len(literal) > 128 || !securityFilterNumber.MatchString(literal) {
		return "", 0, 0, invalid
	}
	negative := strings.HasPrefix(literal, "-")
	literal = strings.TrimPrefix(literal, "-")
	exponent := 0
	if index := strings.IndexAny(literal, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(literal[index+1:], 10, 32)
		if err != nil {
			return "", 0, 0, invalid
		}
		exponent = int(parsed)
		literal = literal[:index]
	}
	scale := -exponent
	if index := strings.IndexByte(literal, '.'); index >= 0 {
		scale += len(literal) - index - 1
		literal = literal[:index] + literal[index+1:]
	}
	digits := strings.TrimLeft(literal, "0")
	if digits == "" {
		return "0", 1, 0, nil
	}
	for scale > 0 && strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		scale--
	}
	precision := max(len(digits), scale)
	if scale < 0 {
		precision = len(digits) - scale
	}
	if precision > 65 || scale > 30 {
		return "", 0, 0, problem(409, "SECURITY_FILTER_PRECISION_UNSUPPORTED", "MySQL行过滤数值超过65位精度或30位小数的安全上限")
	}
	if scale < 0 {
		digits += strings.Repeat("0", -scale)
		scale = 0
	}
	normalized := digits
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		index := len(digits) - scale
		normalized = digits[:index] + "." + digits[index:]
	}
	if negative {
		normalized = "-" + normalized
	}
	return normalized, precision, scale, nil
}

// Doris results are immutable verified outputs. Count and page use the same
// predicates, so neither unfiltered cardinality nor offset gaps are disclosed.
func (s *Server) securityGovernedFilterPage(ctx context.Context, tx *gorm.DB, record Record, filters []SecurityRowFilter, start, size int) ([]DataRow, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(filters) == 0 {
		return nil, 0, invalid
	}
	data, _, run, report, err := s.governanceOutputReady(ctx, tx, record)
	if err != nil {
		return nil, 0, err
	}
	if report.Storage == nil || !validGovernanceStorage(report.Storage, run.ID) {
		return nil, 0, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此成果存储不支持分页前行过滤")
	}
	clause, args, err := securityRowFilterSQL("mysql", filters, data.Definition.Fields, 0)
	if err != nil {
		return nil, 0, err
	}
	db, err := dorisOpen(ctx, false)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	var total int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dorisIdentifier(report.Storage.Database)+"."+dorisIdentifier(report.Storage.Table)+" WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if start > total {
		return nil, 0, invalid
	}
	rows, err := dorisResultPage(ctx, report.Storage, run.ID, data.Definition.Fields, start, size, filters)
	if err != nil {
		return nil, 0, err
	}
	if _, _, _, _, err = s.governanceOutputReady(ctx, tx, record); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
