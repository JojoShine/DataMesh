package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"gorm.io/gorm"
	"io"
)

type sandboxObject struct {
	Fields      []Field           `json:"fields"`
	FieldLabels map[string]string `json:"field_labels"`
	Values      [][]interface{}   `json:"values"`
	Truncated   bool              `json:"truncated"`
}

const sandboxCanonicalMaxBytes = 64 << 20

func sandboxEncodeObject(rows sandboxRows) ([]byte, error) {
	if len(rows.Fields) == 0 || len(rows.Fields) > 100 || len(rows.Values) > lightweightMaxRows {
		return nil, invalid
	}
	for _, row := range rows.Values {
		if len(row) != len(rows.Fields) {
			return nil, invalid
		}
		for _, value := range row {
			if value == nil {
				continue
			}
			text, ok := value.(string)
			if !ok {
				return nil, invalid
			}
			if len(text) > 1<<20 {
				return nil, problem(400, "SANDBOX_CELL_LIMIT", "单个单元格不能超过1MiB")
			}
		}
	}
	data, err := json.Marshal(sandboxObject{rows.Fields, rows.FieldLabels, rows.Values, rows.Truncated})
	if err != nil {
		return nil, err
	}
	if len(data) > sandboxCanonicalMaxBytes {
		return nil, problem(400, "SANDBOX_RESULT_LIMIT", "内部结果超过64MiB")
	}
	return data, nil
}
func (s *Server) sandboxStoreObject(ctx context.Context, rows sandboxRows) (FileConfig, error) {
	data, e := sandboxEncodeObject(rows)
	if e != nil {
		return FileConfig{}, e
	}
	return s.sandboxStoreFile(ctx, "sandbox-result.json", "application/json", data)
}

func (s *Server) sandboxReadObject(ctx context.Context, object FileConfig) (sandboxRows, error) {
	var out sandboxObject
	data, err := s.readStoredFile(ctx, object, sandboxCanonicalMaxBytes)
	if err != nil {
		return sandboxRows{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&out); err != nil {
		return sandboxRows{}, err
	}
	if dec.Decode(new(interface{})) != io.EOF {
		return sandboxRows{}, invalid
	}
	if len(out.Fields) == 0 || len(out.Fields) > 100 || len(out.Values) > lightweightMaxRows {
		return sandboxRows{}, invalid
	}
	for _, row := range out.Values {
		if len(row) != len(out.Fields) {
			return sandboxRows{}, invalid
		}
		for i, v := range row {
			switch value := v.(type) {
			case nil:
			case string:
				if len(value) > 1<<20 {
					return sandboxRows{}, invalid
				}
			case json.Number:
				row[i] = value.String()
			default:
				return sandboxRows{}, invalid
			}
		}
	}
	return sandboxRows{Fields: out.Fields, FieldLabels: out.FieldLabels, Values: out.Values, Truncated: out.Truncated}, nil
}

type sandboxRows struct {
	Fields      []Field
	FieldLabels map[string]string
	Values      [][]interface{}
	Truncated   bool
}

type sandboxObjectContextKey struct{}

func sandboxObjectContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sandboxObjectContextKey{}, id)
}
func (s *Server) sandboxStoreFile(ctx context.Context, name, typ string, data []byte) (FileConfig, error) {
	box, ok := ctx.Value(sandboxObjectContextKey{}).(string)
	if !ok || box == "" {
		return FileConfig{}, invalid
	}
	id, e := newID()
	if e != nil {
		return FileConfig{}, e
	}
	sum := sha256.Sum256(data)
	object := FileConfig{Name: name, MIME: typ, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Key: "resources/sandbox/" + box + "/" + id}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "sandbox-objects:"+box).Error; err != nil {
			return err
		}
		var used int64
		if err := tx.Raw("SELECT COALESCE(SUM((object->>'size')::bigint),0) FROM data_sandbox_objects WHERE sandbox_id=?", box).Scan(&used).Error; err != nil {
			return err
		}
		if used+object.Size > sandboxMaxBytes {
			return problem(409, "SANDBOX_LIMIT", "沙箱容量已达上限")
		}
		return tx.Exec("INSERT INTO data_sandbox_objects(key,sandbox_id,object) VALUES(?,?,?)", object.Key, box, encode(object)).Error
	})
	if e != nil {
		return FileConfig{}, e
	}
	return s.storePrivateFileAt(ctx, name, typ, data, object.Key)
}

type sandboxCSVByteCount int64

func (n *sandboxCSVByteCount) Write(data []byte) (int, error) {
	if int64(*n)+int64(len(data)) > lightweightMaxBytes {
		return 0, problem(400, "SANDBOX_RESULT_LIMIT", "结果CSV超过20MiB")
	}
	*n += sandboxCSVByteCount(len(data))
	return len(data), nil
}
func sandboxCSVSize(rows sandboxRows) (int64, error) {
	var size sandboxCSVByteCount
	writer := csv.NewWriter(&size)
	header := make([]string, len(rows.Fields))
	for i, field := range rows.Fields {
		header[i] = rows.FieldLabels[field.Name]
		if header[i] == "" {
			header[i] = field.Name
		}
	}
	if err := writer.Write(header); err != nil {
		return 0, err
	}
	for _, row := range rows.Values {
		if len(row) != len(header) {
			return 0, invalid
		}
		record := make([]string, len(row))
		for i, value := range row {
			if value == nil {
				continue
			}
			text, ok := value.(string)
			if !ok {
				return 0, invalid
			}
			record[i] = text
		}
		if err := writer.Write(record); err != nil {
			return 0, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return 0, err
	}
	return int64(size), nil
}
