package workflow

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

func exactETLNumber(text string) (*big.Rat, error) {
	if len(text) > 4096 || !snapshotDecimal.MatchString(text) {
		return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "数值无法无损规范化")
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		n, e := strconv.Atoi(text[i+1:])
		if e != nil || n > 4096 || n < -4096 {
			return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "数值指数超过支持范围")
		}
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "数值无法无损规范化")
	}
	return n, nil
}
func etlNumericText(value interface{}, allowFloat bool) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case json.Number:
		return v.String(), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float64:
		if allowFloat && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return strconv.FormatFloat(v, 'g', -1, 64), nil
		}
	case float32:
		if allowFloat && !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) {
			return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
		}
	}
	return "", problem(409, "RECONCILIATION_VALUE_INVALID", "数值类型无法无损规范化")
}
func canonicalETLValue(field Field, value interface{}) ([]byte, error) {
	if value == nil {
		return []byte("null"), nil
	}
	prefix := field.LogicalType + ":"
	switch field.LogicalType {
	case "integer", "decimal":
		text, e := etlNumericText(value, field.LogicalType == "decimal")
		if e != nil {
			return nil, e
		}
		if field.LogicalType == "integer" {
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				return []byte(prefix + strconv.FormatInt(n, 10)), nil
			}
			if n, err := strconv.ParseUint(text, 10, 64); err == nil {
				return []byte(prefix + strconv.FormatUint(n, 10)), nil
			}
		}
		n, e := exactETLNumber(text)
		if e != nil {
			return nil, e
		}
		if field.LogicalType == "integer" && !n.IsInt() {
			return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "整数值包含小数")
		}
		return []byte(prefix + n.RatString()), nil
	case "string":
		switch v := value.(type) {
		case string:
			return []byte(prefix + v), nil
		case []byte:
			return append([]byte(prefix), v...), nil
		}
	case "binary":
		if v, ok := value.([]byte); ok {
			return append([]byte(prefix), v...), nil
		}
	case "boolean":
		switch v := value.(type) {
		case bool:
			return []byte(prefix + strconv.FormatBool(v)), nil
		case int64:
			if v == 0 || v == 1 {
				return []byte(prefix + strconv.FormatBool(v == 1)), nil
			}
		case []byte:
			value = string(v)
		}
		if v, ok := value.(string); ok {
			switch v {
			case "t", "true", "1":
				return []byte(prefix + "true"), nil
			case "f", "false", "0":
				return []byte(prefix + "false"), nil
			}
		}
	case "date", "datetime":
		var stamp time.Time
		haveStamp := false
		var e error
		switch v := value.(type) {
		case time.Time:
			stamp = v
			haveStamp = true
		case []byte:
			value = string(v)
		}
		if !haveStamp {
			if text, ok := value.(string); ok {
				parsed := false
				for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02"} {
					stamp, e = time.Parse(layout, text)
					if e == nil {
						parsed = true
						break
					}
				}
				if !parsed {
					return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "日期时间无法无损规范化")
				}
			} else {
				return nil, problem(409, "RECONCILIATION_VALUE_INVALID", "日期时间类型无效")
			}
		}
		if field.LogicalType == "date" {
			return []byte(prefix + stamp.Format("2006-01-02")), nil
		}
		if field.Precision != nil {
			p := *field.Precision
			if p < 0 || p > 9 {
				return nil, invalid
			}
			unit := time.Second
			for range p {
				unit /= 10
			}
			stamp = stamp.Round(unit)
		}
		return []byte(prefix + stamp.UTC().Format(time.RFC3339Nano)), nil
	}
	return nil, problem(409, "RECONCILIATION_VALUE_INVALID", fmt.Sprintf("字段类型 %s 暂不支持可靠核对", field.LogicalType))
}
func etlRowDigest(key []byte, runID string, fields []Field, values []interface{}) ([]byte, error) {
	if len(key) != 32 || runID == "" || len(fields) != len(values) {
		return nil, invalid
	}
	var body bytes.Buffer
	write := func(raw []byte) { _ = binary.Write(&body, binary.BigEndian, uint64(len(raw))); body.Write(raw) }
	write([]byte("datamesh-etl-row-v1"))
	write([]byte(runID))
	for i, field := range fields {
		write([]byte(field.Name))
		write([]byte(field.LogicalType))
		value, e := canonicalETLValue(field, values[i])
		if e != nil {
			return nil, e
		}
		write(value)
	}
	hash := hmac.New(sha256.New, key)
	hash.Write(body.Bytes())
	return hash.Sum(nil), nil
}
