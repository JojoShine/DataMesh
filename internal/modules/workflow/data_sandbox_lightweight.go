package workflow

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"io"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
)

const sandboxCanonicalLimit = 64 << 20

func (s *Server) readSandboxCanonical(ctx context.Context, object FileConfig, fields, count int) ([][]interface{}, error) {
	data, err := s.readStoredFile(ctx, object, sandboxCanonicalLimit)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	rows := make([][]interface{}, 0, count)
	for {
		var row []interface{}
		err = decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(row) != fields || len(rows) >= count {
			return nil, invalid
		}
		for _, value := range row {
			if value != nil {
				if _, ok := value.(string); !ok {
					return nil, invalid
				}
			}
		}
		rows = append(rows, row)
	}
	if len(rows) != count {
		return nil, invalid
	}
	return rows, nil
}

// The canonical object preserves SQL nulls independently of CSV's empty fields.
func (s *Server) createSandboxLightweightFile(ctx context.Context, p identity.Principal, name string, data sandboxRows) (result lightweightFileDTO, resultErr error) {
	var csvData, canonical bytes.Buffer
	writer := csv.NewWriter(&csvData)
	headers := make([]string, len(data.Fields))
	for i, field := range data.Fields {
		headers[i] = data.FieldLabels[field.Name]
		if headers[i] == "" {
			headers[i] = field.Name
		}
	}
	if err := writer.Write(headers); err != nil {
		return lightweightFileDTO{}, err
	}
	encoder := json.NewEncoder(&canonical)
	for _, row := range data.Values {
		if len(row) != len(headers) {
			return lightweightFileDTO{}, invalid
		}
		text := make([]string, len(row))
		values := make([]interface{}, len(row))
		for i, value := range row {
			if value != nil {
				text[i] = fmt.Sprint(value)
				values[i] = text[i]
			}
		}
		if err := writer.Write(text); err != nil {
			return lightweightFileDTO{}, err
		}
		if err := encoder.Encode(values); err != nil {
			return lightweightFileDTO{}, err
		}
		if canonical.Len() > sandboxCanonicalLimit {
			return lightweightFileDTO{}, invalid
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return lightweightFileDTO{}, err
	}
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		name += ".csv"
	}
	file, err := inspectLightweightCSV(name, csvData.Bytes())
	if err != nil {
		return file, err
	}
	object, err := s.storePrivateFile(ctx, file.Name, "text/csv; charset=utf-8", csvData.Bytes())
	if err != nil {
		return file, err
	}
	objects := []FileConfig{object}
	keep := false
	recordID := ""
	defer func() {
		if keep {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if recordID != "" {
			var existing Record
			lookup := s.db.WithContext(cleanupCtx).Where("kind=? AND id=?", lightweightUploadKind, recordID).Take(&existing).Error
			if !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return
			}
		}
		for _, item := range objects {
			if cleanupErr := s.runtime.store.RemoveObject(cleanupCtx, resourceBucket, item.Key, minio.RemoveObjectOptions{}); cleanupErr != nil {
				resultErr = errors.Join(resultErr, cleanupErr)
			}
		}
	}()
	sidecar, err := s.storePrivateFile(ctx, file.Name+".jsonl", "application/x-ndjson", canonical.Bytes())
	if err != nil {
		return file, err
	}
	objects = append(objects, sidecar)
	record, err := newRecord(lightweightUploadKind, p.DepartmentID, p.UserID, "", "ready", json.RawMessage("null"))
	if err != nil {
		return file, err
	}
	recordID = record.ID
	file.FileID = record.ID
	record.Body = encode(lightweightUpload{File: file, Object: object, Canonical: &sidecar, VisitorScope: sandboxVisitorScope(p)})
	err = s.db.WithContext(ctx).Create(&record).Error
	keep = err == nil
	return file, err
}
