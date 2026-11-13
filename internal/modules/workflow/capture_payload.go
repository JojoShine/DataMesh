package workflow

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type captureSchema struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Field  string          `json:"field"`
	Fields []captureSchema `json:"fields"`
}
type captureEvent struct {
	Before    DataRow         `json:"before"`
	After     DataRow         `json:"after"`
	Source    json.RawMessage `json:"source"`
	Op        string          `json:"op"`
	Timestamp int64           `json:"ts_ms"`
}

func capturePayload(raw []byte) (json.RawMessage, *captureSchema, error) {
	var wrapped map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil, nil, problem(409, "CDC_EVENT_INVALID", "采集事件格式无效")
	}
	payload, hasPayload := wrapped["payload"]
	schema, hasSchema := wrapped["schema"]
	if hasPayload && hasSchema {
		var descriptor captureSchema
		if string(schema) != "null" {
			if json.Unmarshal(schema, &descriptor) != nil || descriptor.Type != "struct" {
				return nil, nil, problem(409, "CDC_SCHEMA_INVALID", "采集事件结构元信息无效")
			}
		}
		return payload, &descriptor, nil
	}
	return raw, nil, nil
}
func normalizeCaptureRow(row DataRow, schema *captureSchema) error {
	if row == nil || schema == nil {
		return nil
	}
	for _, field := range schema.Fields {
		raw, exists := row[field.Field]
		if !exists || string(raw) == "null" {
			continue
		}
		var denominator int64
		switch field.Name {
		case "io.debezium.time.Date", "org.apache.kafka.connect.data.Date":
			days, e := strconv.ParseInt(string(raw), 10, 32)
			if e != nil {
				return problem(409, "CDC_TEMPORAL_INVALID", "日期事件值无法无损解析")
			}
			row[field.Field] = encode(time.Unix(days*86400, 0).UTC().Format("2006-01-02"))
			continue
		case "io.debezium.time.Timestamp", "org.apache.kafka.connect.data.Timestamp":
			denominator = 1000
		case "io.debezium.time.MicroTimestamp":
			denominator = 1000000
		case "io.debezium.time.NanoTimestamp":
			denominator = 1000000000
		case "io.debezium.time.ZonedTimestamp":
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return problem(409, "CDC_TEMPORAL_INVALID", "带时区时间值无效")
			}
			parsed, e := time.Parse(time.RFC3339Nano, value)
			if e != nil {
				return problem(409, "CDC_TEMPORAL_INVALID", "带时区时间值无法无损解析")
			}
			row[field.Field] = encode(parsed.UTC().Format(time.RFC3339Nano))
			continue
		default:
			continue
		}
		value, e := strconv.ParseInt(string(raw), 10, 64)
		if e != nil {
			return problem(409, "CDC_TEMPORAL_INVALID", "时间事件值无法无损解析")
		}
		parsed := time.Unix(value/denominator, (value%denominator)*(1000000000/denominator)).UTC()
		row[field.Field] = encode(strings.TrimSpace(parsed.Format("2006-01-02 15:04:05.999999999")))
	}
	return nil
}
func decodeCaptureEvent(raw []byte) (captureEvent, error) {
	payload, schema, e := capturePayload(raw)
	var event captureEvent
	if e != nil {
		return event, e
	}
	if json.Unmarshal(payload, &event) != nil {
		return event, problem(409, "CDC_EVENT_INVALID", "采集事件无法解析")
	}
	if schema != nil {
		for _, field := range schema.Fields {
			if field.Field == "after" {
				if e = normalizeCaptureRow(event.After, &field); e != nil {
					return event, e
				}
			}
			if field.Field == "before" {
				if e = normalizeCaptureRow(event.Before, &field); e != nil {
					return event, e
				}
			}
		}
	}
	return event, nil
}
func decodeCaptureKey(raw []byte) (DataRow, error) {
	payload, schema, e := capturePayload(raw)
	if e != nil {
		return nil, e
	}
	var key DataRow
	if json.Unmarshal(payload, &key) != nil || len(key) == 0 {
		return nil, problem(409, "CDC_KEY_REQUIRED", "采集事件缺少主键")
	}
	if e = normalizeCaptureRow(key, schema); e != nil {
		return nil, e
	}
	return key, nil
}
