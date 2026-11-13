package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type etlQueryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

var etlCharsetIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func etlTargetColumns(ctx context.Context, db etlQueryer, kind string, target TableConfig, mapping []ETLMapping) ([]etlTargetColumn, error) {
	q := "SELECT column_name,data_type,is_nullable,character_maximum_length,numeric_precision,numeric_scale,datetime_precision,character_set_name,collation_name,collation_schema,'' FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position"
	if kind == "mysql" {
		q = "SELECT column_name,data_type,is_nullable,character_maximum_length,numeric_precision,numeric_scale,datetime_precision,character_set_name,collation_name,NULL,column_type FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position"
	}
	rows, e := db.QueryContext(ctx, q, target.Schema, target.Table)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	columns := map[string]etlTargetColumn{}
	for rows.Next() {
		var name, typ, nullable, columnType string
		var length, precision, scale, fraction sql.NullInt64
		var charset, collation, collationSchema sql.NullString
		if e = rows.Scan(&name, &typ, &nullable, &length, &precision, &scale, &fraction, &charset, &collation, &collationSchema, &columnType); e != nil {
			return nil, e
		}
		if !slices.ContainsFunc(mapping, func(m ETLMapping) bool { return m.Target == name }) {
			continue
		}
		index := slices.IndexFunc(target.Fields, func(f Field) bool { return f.Name == name })
		if index < 0 {
			continue
		}
		field := target.Fields[index]
		if field.LogicalType != sqlLogicalType(typ) {
			return nil, problem(409, "TARGET_SCHEMA_CHANGED", "目标字段类型与当前任务契约不一致，请重新确认映射")
		}
		field.Nullable = nullable == "YES"
		col := etlTargetColumn{Field: field, NativeType: typ, MaxLength: length.Int64, Charset: charset.String, Collation: collation.String, CollationSchema: collationSchema.String, Unsigned: strings.Contains(columnType, "unsigned")}
		if field.LogicalType == "datetime" && fraction.Valid {
			p := int(fraction.Int64)
			col.Field.Precision = &p
		}
		if field.LogicalType == "decimal" {
			col.Field.Precision = nil
			col.Field.Scale = nil
			if precision.Valid {
				p := int(precision.Int64)
				col.Field.Precision = &p
			}
			if scale.Valid {
				v := int(scale.Int64)
				col.Field.Scale = &v
			}
		}
		switch typ {
		case "smallint":
			col.IntegerBits = 16
		case "integer", "int":
			col.IntegerBits = 32
		case "bigint":
			col.IntegerBits = 64
		case "tinyint":
			col.IntegerBits = 8
		case "mediumint":
			col.IntegerBits = 24
		}
		if kind == "postgres" {
			switch typ {
			case "smallint", "integer", "bigint", "boolean", "date", "real", "double precision", "bytea":
				col.Cast = typ
				col.KeyCast = typ
			case "numeric", "decimal":
				col.Cast = "numeric"
				if precision.Valid && scale.Valid {
					if precision.Int64 < 1 || precision.Int64 > 1000 || scale.Int64 < -1000 || scale.Int64 > 1000 {
						return nil, invalid
					}
					col.Cast = fmt.Sprintf("numeric(%d,%d)", precision.Int64, scale.Int64)
				}
				col.KeyCast = col.Cast
			case "uuid":
				col.Cast = "uuid"
				col.KeyCast = "uuid"
			case "text", "character varying", "character":
				col.Cast = "text"
				col.KeyCast = "text"
				col.Char = typ == "character"
				if typ != "text" && length.Valid {
					if length.Int64 < 1 || length.Int64 > 10485760 {
						return nil, invalid
					}
					col.KeyCast = fmt.Sprintf("%s(%d)", typ, length.Int64)
				}
			case "timestamp with time zone", "timestamp without time zone":
				p := int64(6)
				if fraction.Valid {
					p = fraction.Int64
				}
				if p < 0 || p > 6 {
					return nil, invalid
				}
				zone := "without"
				if typ == "timestamp with time zone" {
					zone = "with"
				}
				col.Cast = fmt.Sprintf("timestamp(%d) %s time zone", p, zone)
				col.KeyCast = col.Cast
			}
		} else {
			if col.IntegerBits > 0 {
				col.Cast = "SIGNED"
				if col.Unsigned {
					col.Cast = "UNSIGNED"
				}
				col.KeyCast = col.Cast
			}
			switch typ {
			case "decimal", "numeric":
				if !precision.Valid || !scale.Valid || precision.Int64 < 1 || precision.Int64 > 65 || scale.Int64 < 0 || scale.Int64 > 30 {
					return nil, invalid
				}
				col.Cast = fmt.Sprintf("DECIMAL(%d,%d)", precision.Int64, scale.Int64)
				col.KeyCast = col.Cast
			case "float", "double":
				col.Cast = "DOUBLE"
				col.KeyCast = "DOUBLE"
			case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
				col.Cast = "CHAR"
				col.KeyCast = "CHAR"
				col.Char = typ == "char"
			case "date":
				col.Cast = "DATE"
				col.KeyCast = "DATE"
			case "datetime", "timestamp":
				p := fraction.Int64
				if p < 0 || p > 6 {
					return nil, invalid
				}
				col.Cast = fmt.Sprintf("DATETIME(%d)", p)
				col.KeyCast = col.Cast
			case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
				col.Cast = "BINARY"
				col.KeyCast = "BINARY"
			}
			if col.Field.LogicalType == "string" && (!etlCharsetIdentifier.MatchString(col.Charset) || !etlCharsetIdentifier.MatchString(col.Collation)) {
				return nil, problem(409, "RECONCILIATION_TYPE_UNSUPPORTED", "目标字符集或排序规则无法核验")
			}
		}
		if col.Cast == "" {
			return nil, problem(409, "RECONCILIATION_TYPE_UNSUPPORTED", "目标字段的数据库类型尚不支持可靠核对")
		}
		columns[name] = col
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := make([]etlTargetColumn, 0, len(mapping))
	for _, m := range mapping {
		col, ok := columns[m.Target]
		if !ok {
			return nil, problem(409, "TARGET_SCHEMA_CHANGED", "目标映射字段已变化")
		}
		out = append(out, col)
	}
	return out, nil
}
func etlKeyExpression(kind string, col etlTargetColumn, expression string) string {
	out := "CAST(" + expression + " AS " + col.KeyCast + ")"
	if col.Field.LogicalType != "string" {
		return out
	}
	if kind == "mysql" {
		out = "CONVERT(" + expression + " USING " + col.Charset + ")"
		if col.Char {
			out = "RTRIM(" + out + ")"
		}
		return out + " COLLATE " + col.Collation
	}
	if col.Collation != "" {
		out += " COLLATE " + quoteIdentifier(kind, col.CollationSchema) + "." + quoteIdentifier(kind, col.Collation)
	}
	return out
}
func etlExpectedQuery(kind string, target TableConfig, mapping []ETLMapping, columns []etlTargetColumn, batch [][]interface{}) (string, []interface{}, error) {
	return etlDeliveryQuery(kind, target, mapping, columns, batch, false)
}
func etlDeliveryQuery(kind string, target TableConfig, mapping []ETLMapping, columns []etlTargetColumn, batch [][]interface{}, actual bool) (string, []interface{}, error) {
	if len(batch) == 0 || len(batch) > 500 || len(columns) != len(mapping) {
		return "", nil, invalid
	}
	inputMapping := mapping
	inputColumns := columns
	if actual {
		inputMapping = nil
		inputColumns = nil
		for _, key := range target.Keys {
			i := slices.IndexFunc(mapping, func(m ETLMapping) bool { return m.Target == key })
			if i < 0 {
				return "", nil, invalid
			}
			inputMapping = append(inputMapping, mapping[i])
			inputColumns = append(inputColumns, columns[i])
		}
	}
	names := []string{"seq"}
	for i := range inputMapping {
		names = append(names, "c"+strconv.Itoa(i))
	}
	rows := []string{}
	args := []interface{}{}
	for seq, values := range batch {
		if len(values) != len(inputColumns) {
			return "", nil, invalid
		}
		items := []string{strconv.Itoa(seq)}
		for i, value := range values {
			placeholder := "?"
			if kind == "postgres" {
				placeholder = "$" + strconv.Itoa(len(args)+1)
			}
			items = append(items, "CAST("+placeholder+" AS "+inputColumns[i].Cast+")")
			args = append(args, value)
		}
		if kind == "postgres" {
			rows = append(rows, "("+strings.Join(items, ",")+")")
		} else {
			for i := range items {
				items[i] += " AS " + names[i]
			}
			rows = append(rows, "SELECT "+strings.Join(items, ","))
		}
	}
	table := quoteIdentifier(kind, target.Schema) + "." + quoteIdentifier(kind, target.Table)
	expected := []string{"incoming.seq"}
	join := []string{}
	checks := []string{}
	for _, key := range target.Keys {
		i := slices.IndexFunc(inputMapping, func(m ETLMapping) bool { return m.Target == key })
		if i < 0 {
			return "", nil, invalid
		}
		expected = append(expected, "t."+quoteIdentifier(kind, key))
		expr := "incoming.c" + strconv.Itoa(i)
		join = append(join, "t."+quoteIdentifier(kind, key)+"="+etlKeyExpression(kind, inputColumns[i], expr))
		col := inputColumns[i]
		if kind == "mysql" && col.Field.LogicalType == "string" {
			checks = append(checks, "BINARY CONVERT(CONVERT("+expr+" USING "+col.Charset+") USING utf8mb4)=BINARY "+expr)
		}
	}
	for i, m := range mapping {
		expr := "incoming.c" + strconv.Itoa(i)
		if actual || slices.Contains(target.Keys, m.Target) {
			expr = "t." + quoteIdentifier(kind, m.Target)
		}
		expected = append(expected, expr)
	}
	from := ""
	if kind == "postgres" {
		from = "WITH incoming(" + strings.Join(names, ",") + ") AS (VALUES " + strings.Join(rows, ",") + ") SELECT " + strings.Join(expected, ",") + " FROM incoming"
	} else {
		from = "SELECT " + strings.Join(expected, ",") + " FROM (" + strings.Join(rows, " UNION ALL ") + ") incoming"
	}
	q := from + " JOIN " + table + " t ON " + strings.Join(join, " AND ")
	if kind == "postgres" {
		projection := make([]string, 0, len(mapping))
		for _, m := range mapping {
			projection = append(projection, "t."+quoteIdentifier(kind, m.Target))
		}
		// Keep each lookup correlated when statistics describe an empty target.
		// Two rows preserve multiplicity detection without scanning the full table.
		q = from + " JOIN LATERAL (SELECT " + strings.Join(projection, ",") + " FROM " + table + " t WHERE " + strings.Join(join, " AND ") + " LIMIT 2) t ON TRUE"
	}
	if len(checks) > 0 {
		q += " WHERE " + strings.Join(checks, " AND ")
	}
	return q + " ORDER BY incoming.seq", args, nil
}
func etlValidateTargetValues(columns []etlTargetColumn, values []interface{}) error {
	for i, col := range columns {
		value := values[i]
		if value == nil {
			continue
		}
		if col.Field.LogicalType == "string" && col.MaxLength > 0 {
			var text string
			switch v := value.(type) {
			case string:
				text = v
			case []byte:
				text = string(v)
			default:
				return invalid
			}
			if col.Char {
				text = strings.TrimRight(text, " ")
			}
			if int64(utf8.RuneCountInString(text)) > col.MaxLength {
				return problem(409, "RECONCILIATION_VALUE_LOSS", "映射文本超过目标声明长度，已阻止截断")
			}
		}
		if col.IntegerBits > 0 {
			text, e := etlNumericText(value, false)
			if e != nil {
				return e
			}
			fits := func(text string) bool {
				if col.Unsigned {
					_, err := strconv.ParseUint(text, 10, col.IntegerBits)
					return err == nil
				}
				_, err := strconv.ParseInt(text, 10, col.IntegerBits)
				return err == nil
			}
			if !fits(text) {
				n, err := exactETLNumber(text)
				if err != nil || !n.IsInt() || !fits(n.Num().String()) {
					return problem(409, "RECONCILIATION_VALUE_LOSS", "映射整数超过目标存储范围或无法无损解析")
				}
				values[i] = n.Num().String()
			}
		}
		if col.Field.LogicalType == "decimal" && col.Field.Precision != nil && col.Field.Scale != nil {
			text, e := etlNumericText(value, true)
			if e != nil {
				return e
			}
			n, e := exactETLNumber(text)
			if e != nil {
				return e
			}
			scale := *col.Field.Scale
			factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(scale))), nil)
			scaled := new(big.Rat).Set(n)
			if scale >= 0 {
				scaled.Mul(scaled, new(big.Rat).SetInt(factor))
			} else {
				scaled.Quo(scaled, new(big.Rat).SetInt(factor))
			}
			if !scaled.IsInt() || len(new(big.Int).Abs(scaled.Num()).String()) > *col.Field.Precision {
				return problem(409, "RECONCILIATION_VALUE_LOSS", "映射小数超过目标精度或范围")
			}
		}
	}
	return nil
}
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
