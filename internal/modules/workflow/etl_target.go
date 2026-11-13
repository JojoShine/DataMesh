package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

// A database-side lock also covers separate source registrations that alias the same target.
func etlTargetTransaction(ctx context.Context, db *sql.DB, kind string, target TableConfig) (*sql.Tx, func(), error) {
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, nil, e
	}
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		conn.Close()
		return nil, nil, e
	}
	names := []string{target.Schema, target.Table}
	if kind == "mysql" {
		for i := range names {
			names[i] = strings.ToLower(names[i])
		}
	}
	digest := sha256.Sum256(encode(names))
	name := hex.EncodeToString(digest[:])
	mysqlLocked := false
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = tx.Rollback()
			if mysqlLocked {
				cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				var unlocked sql.NullInt64
				err := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", name).Scan(&unlocked)
				cancel()
				if err != nil || !unlocked.Valid || unlocked.Int64 != 1 {
					_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
					slog.Error("ETL target lock release unavailable")
				}
			}
			_ = conn.Close()
		})
	}
	if e = etlSessionTimeZone(ctx, tx, kind); e != nil {
		release()
		return nil, nil, e
	}
	var locked bool
	if kind == "postgres" {
		// Replan bounded key lookups as an initially empty target grows, instead of reusing its small-table scan plan.
		var planMode sql.NullString
		e = tx.QueryRowContext(ctx, "SELECT current_setting('plan_cache_mode',true)").Scan(&planMode)
		if e == nil && planMode.Valid {
			_, e = tx.ExecContext(ctx, "SET LOCAL plan_cache_mode='force_custom_plan'")
		}
		if e == nil {
			e = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(digest[:8]))).Scan(&locked)
		}
	} else {
		var result sql.NullInt64
		e = tx.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&result)
		locked = result.Valid && result.Int64 == 1
		mysqlLocked = locked
	}
	if e != nil {
		release()
		return nil, nil, e
	}
	if !locked {
		release()
		return nil, nil, problem(409, "ETL_TARGET_BUSY", "目标表存在未结束事务，请待原任务结束后重试")
	}
	return tx, release, nil
}
func (s *Server) etlCheckTargetIdle(ctx context.Context, db *gorm.DB, p identity.Principal, run ETLRun, ctl ETLControl) (func(), error) {
	if ctl.TargetKey == "" {
		return func() {}, nil
	}
	_, src, to, e := s.sourceForRead(ctx, db, p, run.Definition.Target.SourceID)
	if e != nil {
		return nil, e
	}
	_, release, e := etlTargetTransaction(ctx, to, src.Kind, run.Definition.Target)
	if e != nil {
		to.Close()
		return nil, e
	}
	return func() { release(); to.Close() }, nil
}

func etlSessionTimeZone(ctx context.Context, tx *sql.Tx, kind string) error {
	if kind == "mysql" {
		_, e := tx.ExecContext(ctx, "SET time_zone='+00:00'")
		return e
	}
	return nil
}
