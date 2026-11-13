package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type GovernanceStorage struct {
	Engine    string `json:"engine"`
	Database  string `json:"database"`
	Table     string `json:"table"`
	DataLayer string `json:"data_layer"`
}

var dorisObjectID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var indexedParameter = regexp.MustCompile(`\$([1-9][0-9]*)`)

func validDataLayer(layer string) bool {
	switch layer {
	case "", "ods", "dwd", "dim", "dws", "ads":
		return true
	}
	return false
}
func outputDataLayer(layer string) string {
	if layer == "" {
		return "dwd"
	}
	return layer
}
func dorisIdentifier(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

var dorisNamespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}_$`)
var dorisUserPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validateDorisConfiguration() error {
	prefix := os.Getenv("DORIS_DATABASE_PREFIX")
	if prefix != "" && !dorisNamespacePattern.MatchString(prefix) {
		return fmt.Errorf("invalid Doris database namespace")
	}
	for _, name := range []string{"DORIS_READER_USER", "DORIS_WRITER_USER", "DORIS_WORKLOAD_GROUP"} {
		if value := os.Getenv(name); value != "" && !dorisUserPattern.MatchString(value) {
			return fmt.Errorf("invalid Doris service identity")
		}
	}
	if prefix != "" && prefix != "datamesh_" {
		reader, writer := os.Getenv("DORIS_READER_USER"), os.Getenv("DORIS_WRITER_USER")
		if reader == "" || writer == "" || reader == writer || reader == "root" || writer == "root" || reader == "datamesh_reader" || reader == "datamesh_writer" || writer == "datamesh_reader" || writer == "datamesh_writer" {
			return fmt.Errorf("custom Doris namespace requires separate service identities")
		}
	}
	for name, bounds := range map[string][2]int64{"DORIS_QUERY_MEMORY_BYTES": {16 << 20, 1 << 30}, "DORIS_QUERY_TIMEOUT_SECONDS": {1, 300}} {
		if value := os.Getenv(name); value != "" {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < bounds[0] || number > bounds[1] {
				return fmt.Errorf("invalid Doris query budget")
			}
		}
	}
	return nil
}
func dorisDatabase(layer string) string {
	prefix := os.Getenv("DORIS_DATABASE_PREFIX")
	if prefix == "" {
		prefix = "datamesh_"
	}
	return prefix + layer
}
func dorisUser(write bool) string {
	name, fallback := "DORIS_READER_USER", "datamesh_reader"
	if write {
		name, fallback = "DORIS_WRITER_USER", "datamesh_writer"
	}
	if user := os.Getenv(name); user != "" {
		return user
	}
	return fallback
}
func dorisAddress() string {
	if address := os.Getenv("GOVERNANCE_DORIS_ADDRESS"); address != "" {
		return address
	}
	return "172.29.238.10:9030"
}
func configureDorisSession(cfg *mysql.Config) error {
	if err := validateDorisConfiguration(); err != nil {
		return err
	}
	for name, setting := range map[string]string{"DORIS_QUERY_MEMORY_BYTES": "exec_mem_limit", "DORIS_QUERY_TIMEOUT_SECONDS": "query_timeout"} {
		if raw := os.Getenv(name); raw != "" {
			number, _ := strconv.ParseInt(raw, 10, 64)
			if cfg.Params == nil {
				cfg.Params = map[string]string{}
			}
			cfg.Params[setting] = strconv.FormatInt(number, 10)
		}
	}
	if group := os.Getenv("DORIS_WORKLOAD_GROUP"); group != "" {
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params["workload_group"] = "'" + group + "'"
	}
	return nil
}
func dorisOpen(ctx context.Context, write bool) (*sql.DB, error) {
	if err := validateDorisConfiguration(); err != nil {
		return nil, err
	}
	cfg := mysql.NewConfig()
	cfg.User = dorisUser(false)
	cfg.Passwd = os.Getenv("DORIS_READER_PASSWORD")
	if write {
		cfg.User = dorisUser(true)
		cfg.Passwd = os.Getenv("DORIS_WRITER_PASSWORD")
	}
	if cfg.Passwd == "" {
		return nil, problem(503, "DORIS_UNCONFIGURED", "Doris 服务身份尚未配置")
	}
	cfg.Net = "tcp"
	cfg.Addr = dorisAddress()
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 2 * time.Minute
	cfg.WriteTimeout = 30 * time.Second
	cfg.InterpolateParams = true
	if err := configureDorisSession(cfg); err != nil {
		return nil, err
	}
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(3)
	db.SetConnMaxLifetime(5 * time.Minute)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, problem(503, "DORIS_UNAVAILABLE", "Doris 暂不可用，本次不会改用平台数据库处理")
	}
	return db, nil
}

// Resolve indexed parameters without interpolating values into SQL. Repeated
// parameters are deliberately duplicated for the MySQL positional protocol.
func dorisParameters(query string, args []interface{}) (string, []interface{}, error) {
	values := []interface{}{}
	var bad bool
	query = indexedParameter.ReplaceAllStringFunc(query, func(mark string) string {
		n, e := strconv.Atoi(mark[1:])
		if e != nil || n < 1 || n > len(args) {
			bad = true
			return "?"
		}
		values = append(values, args[n-1])
		return "?"
	})
	if bad {
		return "", nil, invalid
	}
	return query, values, nil
}

type governanceDorisStage struct {
	db         *sql.DB
	table, ods string
}

func newGovernanceDorisStage(ctx context.Context, id string) (*governanceDorisStage, error) {
	if !dorisObjectID.MatchString(id) {
		return nil, invalid
	}
	db, e := dorisOpen(ctx, true)
	if e != nil {
		return nil, e
	}
	stage := &governanceDorisStage{db: db, table: dorisDatabase("internal") + ".stage_" + id, ods: dorisDatabase("ods") + ".input_" + id}
	created := []string{}
	for _, table := range []string{stage.table, stage.ods} {
		// Run identities are never reused. IF NOT EXISTS would conceal a partial retry.
		if _, e = db.ExecContext(ctx, "CREATE TABLE "+table+" (node_id VARCHAR(128) NOT NULL,port VARCHAR(32) NOT NULL DEFAULT '',ordinal BIGINT NOT NULL,payload JSON NOT NULL,violations STRING NOT NULL DEFAULT '[]') UNIQUE KEY(node_id,port,ordinal) DISTRIBUTED BY HASH(node_id,ordinal) BUCKETS 1 PROPERTIES ('replication_num'='1','enable_unique_key_merge_on_write'='true')"); e != nil {
			for _, table := range created {
				if _, cleanupErr := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); cleanupErr != nil {
					slog.Warn("governance stage initialization cleanup failed", "run_id", id)
				}
			}
			db.Close()
			return nil, e
		}
		created = append(created, table)
	}
	return stage, nil
}
func (s *governanceDorisStage) sql(query string, args []interface{}) (string, []interface{}, error) {
	return dorisParameters(strings.ReplaceAll(query, "governance_flow_stage", s.table), args)
}
func (s *governanceDorisStage) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	q, a, e := s.sql(query, args)
	if e != nil {
		return nil, e
	}
	return s.db.ExecContext(ctx, q, a...)
}
func (s *governanceDorisStage) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	q, a, e := s.sql(query, args)
	if e != nil {
		return nil, e
	}
	return s.db.QueryContext(ctx, q, a...)
}

type governanceRowScanner interface{ Scan(...interface{}) error }
type governanceRowError struct{ err error }

func (r governanceRowError) Scan(...interface{}) error { return r.err }
func (s *governanceDorisStage) QueryRowContext(ctx context.Context, query string, args ...interface{}) governanceRowScanner {
	q, a, e := s.sql(query, args)
	if e != nil {
		return governanceRowError{err: e}
	}
	return s.db.QueryRowContext(ctx, q, a...)
}
func (s *governanceDorisStage) close() { s.db.Close() }
func (s *governanceDorisStage) drop(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+s.table)
	return e
}
func (s *governanceDorisStage) sealSource(ctx context.Context, node string) error {
	_, e := s.ExecContext(ctx, "INSERT INTO "+s.ods+" SELECT * FROM governance_flow_stage WHERE node_id=$1", node)
	return e
}
func (s *governanceDorisStage) publish(ctx context.Context, id, node, layer string, fields []Field) (*GovernanceStorage, error) {
	if !dorisObjectID.MatchString(id) || !validDataLayer(layer) {
		return nil, invalid
	}
	layer = outputDataLayer(layer)
	binding := &GovernanceStorage{Engine: "doris", Database: dorisDatabase(layer), Table: "result_" + id, DataLayer: layer}
	columns := []string{"`_dm_ordinal` BIGINT NOT NULL"}
	values := []string{"l.ordinal"}
	q := &governanceDorisSQL{}
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "_dm_") {
			return nil, problem(409, "DORIS_RESERVED_FIELD", "字段名称不能使用 _dm_ 内部前缀")
		}
		columns = append(columns, dorisIdentifier(f.Name)+" "+dorisFieldType(f)+" NULL")
		values = append(values, q.value("l", f))
	}
	table := binding.Database + "." + binding.Table
	if _, e := s.db.ExecContext(ctx, "CREATE TABLE "+table+" ("+strings.Join(columns, ",")+") UNIQUE KEY(`_dm_ordinal`) DISTRIBUTED BY HASH(`_dm_ordinal`) BUCKETS 1 PROPERTIES ('replication_num'='1','enable_unique_key_merge_on_write'='true')"); e != nil {
		return nil, e
	}
	where := q.input("l", GovernanceFlowEdge{Node: node})
	if _, e := s.ExecContext(ctx, "INSERT INTO "+table+" SELECT "+strings.Join(values, ",")+" FROM governance_flow_stage l WHERE "+where, q.args...); e != nil {
		return nil, e
	}
	rejected := dorisDatabase("quality") + ".rejected_" + id
	if _, e := s.db.ExecContext(ctx, "CREATE TABLE "+rejected+" LIKE "+s.table); e != nil {
		return nil, e
	}
	if _, e := s.ExecContext(ctx, "INSERT INTO "+rejected+" SELECT * FROM governance_flow_stage WHERE port='bad'"); e != nil {
		return nil, e
	}
	return binding, nil
}
func validGovernanceStorage(b *GovernanceStorage, runID string) bool {
	return b != nil && b.Engine == "doris" && dorisObjectID.MatchString(runID) && b.Table == "result_"+runID && b.DataLayer != "" && validDataLayer(b.DataLayer) && b.Database == dorisDatabase(b.DataLayer)
}
func dorisResultCount(ctx context.Context, b *GovernanceStorage, id string) (int64, error) {
	if !validGovernanceStorage(b, id) {
		return 0, invalid
	}
	db, e := dorisOpen(ctx, false)
	if e != nil {
		return 0, e
	}
	defer db.Close()
	var n int64
	e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+b.Database+"."+b.Table).Scan(&n)
	return n, e
}
func dorisResultPage(ctx context.Context, b *GovernanceStorage, id string, fields []Field, start, size int, rowFilters ...[]SecurityRowFilter) ([]DataRow, error) {
	if !validGovernanceStorage(b, id) || start < 0 || size < 1 || size > 1000 {
		return nil, invalid
	}
	db, e := dorisOpen(ctx, false)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	pairs := []string{}
	args := []interface{}{}
	for _, f := range fields {
		v := dorisIdentifier(f.Name)
		switch f.LogicalType {
		case "integer", "decimal", "date":
			v = "CAST(" + v + " AS STRING)"
		case "datetime":
			v = "CONCAT(REPLACE(CAST(" + v + " AS STRING),' ','T'),'Z')"
		}
		pairs = append(pairs, "?", v)
		args = append(args, f.Name)
	}
	filters := []SecurityRowFilter{}
	if len(rowFilters) > 0 {
		filters = rowFilters[0]
	}
	filterSQL, filterArgs, e := securityRowFilterSQL("mysql", filters, fields, len(args))
	if e != nil {
		return nil, e
	}
	where := ""
	if filterSQL != "" {
		where = " WHERE " + filterSQL
	}
	args = append(args, filterArgs...)
	rows, e := db.QueryContext(ctx, "SELECT JSON_OBJECT("+strings.Join(pairs, ",")+") FROM "+b.Database+"."+b.Table+where+" ORDER BY `_dm_ordinal` LIMIT "+fmt.Sprint(size)+" OFFSET "+fmt.Sprint(start), args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DataRow{}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		row, err := decode[DataRow](raw)
		if err != nil {
			return nil, err
		}
		if err = normalizeSnapshotNumbers(row, Object{Fields: fields}); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
