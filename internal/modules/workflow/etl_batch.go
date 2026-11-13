package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
)

type etlLimits struct {
	MaxRows  int           `json:"max_rows"`
	MaxBytes int64         `json:"max_bytes"`
	Timeout  time.Duration `json:"-"`
}

func loadETLLimits() (etlLimits, error) {
	out := etlLimits{MaxRows: 2000000, MaxBytes: 1024 * 1024 * 1024, Timeout: 30 * time.Minute}
	read := func(name string, def, min, max int64) (int64, error) {
		raw := os.Getenv(name)
		if raw == "" {
			return def, nil
		}
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < min || n > max {
			return 0, fmt.Errorf("invalid %s", name)
		}
		return n, nil
	}
	rows, e := read("ETL_MAX_ROWS", int64(out.MaxRows), 1, 10000000)
	if e != nil {
		return out, e
	}
	out.MaxRows = int(rows)
	out.MaxBytes, e = read("ETL_MAX_BYTES", out.MaxBytes, 1024, 16*1024*1024*1024)
	if e != nil {
		return out, e
	}
	seconds, e := read("ETL_TIMEOUT_SECONDS", 1800, 5, 7200)
	if e != nil {
		return out, e
	}
	out.Timeout = time.Duration(seconds) * time.Second
	return out, nil
}
func etlValueBytes(values []interface{}) int64 {
	var size int64
	for _, v := range values {
		switch x := v.(type) {
		case string:
			size += int64(len(x))
		case []byte:
			size += int64(len(x))
		default:
			size += 32
		}
	}
	return size
}

// The source snapshot stays open while bounded batches flow into one target transaction.
func (s *Server) etlReader(ctx context.Context, from *sql.DB, src SourceSecret, p identity.Principal, run *ETLRun) (func(func([]interface{}) error) error, func(), error) {
	d := run.Definition
	if d.SourceResource != nil {
		return s.etlResourceReader(ctx, p, run)
	}
	if src.Kind == "mysql" {
		var engine string
		if e := from.QueryRowContext(ctx, "SELECT COALESCE(engine,'') FROM information_schema.tables WHERE table_schema=? AND table_name=?", d.Source.Schema, d.Source.Table).Scan(&engine); e != nil {
			return nil, nil, e
		}
		if engine != "InnoDB" {
			return nil, nil, problem(400, "ETL_TRANSACTION_REQUIRED", "MySQL来源必须为InnoDB，以保证一致性读取")
		}
	}
	sourceTx, e := from.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, nil, e
	}
	close := func() { _ = sourceTx.Rollback() }
	if e = etlSessionTimeZone(ctx, sourceTx, src.Kind); e != nil {
		close()
		return nil, nil, e
	}
	if run.InputSnapshot == nil {
		id, err := newID()
		if err != nil {
			close()
			return nil, nil, err
		}
		source, err := get(s.db.WithContext(ctx), "source", d.Source.SourceID, false)
		if err != nil {
			close()
			return nil, nil, err
		}
		fields := []string{}
		for _, m := range d.Mapping {
			if !slices.Contains(fields, m.Source) {
				fields = append(fields, m.Source)
			}
		}
		run.InputSnapshot = &ETLInputSnapshot{Kind: "database", ResourceID: d.Source.SourceID, ContractVersion: source.Version, SnapshotID: id, Fields: fields}
	}
	table := quoteIdentifier(src.Kind, d.Source.Schema) + "." + quoteIdentifier(src.Kind, d.Source.Table)
	filterSQL, filterArgs, err := securityRowFilterSQL(src.Kind, run.securityRowFilters, d.Source.Fields, 0)
	if err != nil {
		close()
		return nil, nil, err
	}
	where := ""
	if filterSQL != "" {
		where = " WHERE " + filterSQL
	}
	q := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s%s LIMIT %d) AS bounded_source", table, where, s.etl.MaxRows+1)
	if e = sourceTx.QueryRowContext(ctx, q, filterArgs...).Scan(&run.Planned); e != nil {
		close()
		return nil, nil, e
	}
	if run.Planned > s.etl.MaxRows {
		close()
		return nil, nil, problem(409, "ETL_LIMIT", "来源行数超过配置上限")
	}
	cols, order := []string{}, []string{}
	for _, m := range d.Mapping {
		cols = append(cols, quoteIdentifier(src.Kind, m.Source))
	}
	for _, key := range d.Source.Keys {
		order = append(order, quoteIdentifier(src.Kind, key))
	}
	reader := func(consume func([]interface{}) error) error {
		rows, e := sourceTx.QueryContext(ctx, "SELECT "+strings.Join(cols, ",")+" FROM "+table+where+" ORDER BY "+strings.Join(order, ","), filterArgs...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			values := make([]interface{}, len(d.Mapping))
			ptrs := make([]interface{}, len(values))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				return e
			}
			for i, m := range d.Mapping {
				values[i], e = transformETL(values[i], m.Rule)
				if e != nil {
					return e
				}
			}
			if e = consume(values); e != nil {
				return e
			}
		}
		return rows.Err()
	}
	return reader, close, nil
}

// PostgreSQL may normalize distinct input keys to the same database key (for example char padding).
// A savepoint allows preserving the original ordered upsert semantics without abandoning batching.
func etlWriteBatch(ctx context.Context, tx *sql.Tx, kind string, target TableConfig, mapping []ETLMapping, batch [][]interface{}) error {
	if len(batch) == 0 {
		return nil
	}
	if kind == "postgres" {
		if _, e := tx.ExecContext(ctx, "SAVEPOINT etl_batch"); e != nil {
			return e
		}
	}
	args := make([]interface{}, 0, len(batch)*len(mapping))
	for _, row := range batch {
		args = append(args, row...)
	}
	q := etlBatchInsert(kind, target, mapping, len(batch))
	if q == "" {
		return invalid
	}
	_, e := tx.ExecContext(ctx, q, args...)
	if e != nil {
		var pg *pgconn.PgError
		if kind != "postgres" || !errors.As(e, &pg) || pg.Code != "21000" {
			return e
		}
		if _, e = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT etl_batch"); e != nil {
			return e
		}
		single := etlInsert(kind, target, mapping)
		for _, row := range batch {
			if _, e = tx.ExecContext(ctx, single, row...); e != nil {
				return e
			}
		}
	}
	if kind == "postgres" {
		_, e = tx.ExecContext(ctx, "RELEASE SAVEPOINT etl_batch")
	}
	return e
}
