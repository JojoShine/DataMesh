package workflow

import (
	"context"
	"database/sql"
	"time"
)

// Parse every non-null value: lexical maxima are unreliable when offsets differ.
func profileBusinessTime(ctx context.Context, stage *sql.Tx, field string, guard func() error) (*time.Time, string, error) {
	rows, e := stage.QueryContext(ctx, "SELECT payload->>$1 FROM governance_input_stage WHERE payload->>$1 IS NOT NULL ORDER BY ordinal", field)
	if e != nil {
		return nil, "unknown", e
	}
	defer rows.Close()
	var maximum *time.Time
	count := 0
	invalidValue := false
	for rows.Next() {
		if count%500 == 0 {
			if e = guard(); e != nil {
				return nil, "unknown", e
			}
		}
		count++
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, "unknown", e
		}
		var value time.Time
		parsed := false
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
			if timestamp, e := time.Parse(layout, raw); e == nil {
				value = timestamp
				parsed = true
				break
			}
		}
		if !parsed {
			invalidValue = true
			continue
		}
		if maximum == nil || value.After(*maximum) {
			copy := value
			maximum = &copy
		}
	}
	if e = rows.Err(); e != nil {
		return nil, "unknown", e
	}
	if invalidValue || maximum == nil {
		return nil, "unknown", nil
	}
	return maximum, "verified", nil
}
