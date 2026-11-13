package integration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
	"github.com/go-sql-driver/mysql"
)

func TestETLMySQLExchange(t *testing.T) {
	if os.Getenv("MYSQL_ETL_TEST_DATABASE") == "" {
		t.Skip("run through scripts/test_identity.py --run TestETLMySQLExchange")
	}
	targetURL, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	pgPort, _ := strconv.Atoi(targetURL.Port())
	pgDB := strings.TrimPrefix(targetURL.Path, "/")
	myDB := os.Getenv("MYSQL_ETL_TEST_DATABASE")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": targetURL.Hostname(), "port": pgPort, "database": pgDB}, {"kind": "mysql", "host": "mysql-source", "port": 3306, "database": myDB}})))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "mysql_etl_", "test-mysql", []string{"admin"})
	cookie, p := login(t, router, name)
	cfg := mysql.NewConfig()
	cfg.User = os.Getenv("MYSQL_ETL_TEST_USER")
	cfg.Passwd = os.Getenv("MYSQL_ETL_TEST_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = "mysql-source:3306"
	cfg.DBName = myDB
	my, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer my.Close()
	for _, table := range []string{"source", "target"} {
		if e = db.Exec("CREATE TABLE public." + table + "(id bigint PRIMARY KEY,name varchar(100),amount numeric(20,6))").Error; e != nil {
			t.Fatal(e)
		}
		if _, e = my.Exec("CREATE TABLE `" + table + "`(id bigint PRIMARY KEY,name varchar(100),amount decimal(20,6)) ENGINE=InnoDB"); e != nil {
			t.Fatal(e)
		}
	}
	if e = db.Exec("INSERT INTO public.source SELECT 9007199254740993+i,'  Alpha  ',1234.567890 FROM generate_series(1,1501) i").Error; e != nil {
		t.Fatal(e)
	}
	for first := 1; first <= 1501; first += 500 {
		values := []string{}
		args := []interface{}{}
		for i := first; i < first+500 && i <= 1501; i++ {
			values = append(values, "(?,?,?)")
			args = append(args, int64(9007199254740993)+int64(i), "  Alpha  ", "1234.567890")
		}
		if _, e = my.Exec("INSERT INTO source VALUES "+strings.Join(values, ","), args...); e != nil {
			t.Fatal(e)
		}
	}
	post := func(path string, value interface{}, status int) json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, value), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	password, _ := targetURL.User.Password()
	ids := []string{}
	for _, input := range []workflow.SourceInput{{Name: "PG batch", Kind: "postgres", Host: targetURL.Hostname(), Port: pgPort, Database: pgDB, OwnerID: uid, Credentials: workflow.Credential{Username: targetURL.User.Username(), Password: password}}, {Name: "MySQL batch", Kind: "mysql", Host: "mysql-source", Port: 3306, Database: myDB, OwnerID: uid, Credentials: workflow.Credential{Username: cfg.User, Password: cfg.Passwd}}} {
		var out struct {
			ID string `json:"source_id"`
		}
		if e = json.Unmarshal(post("/api/v1/sources", input, 201), &out); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, out.ID)
	}
	assertNativeMetadataColumn(t, db, router, cookie, p, workflow.Object{ObjectID: "mysql-metadata-" + ids[1], SourceID: ids[1], QualifiedName: myDB + ".source", SchemaVersion: 1, Fields: []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true}, {Name: "amount", LogicalType: "decimal", Nullable: true}}}, "name", 100)
	for _, pair := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}} {
		t.Run(fmt.Sprintf("%d_to_%d", pair[0], pair[1]), func(t *testing.T) {
			schemas := []string{"public", myDB}
			if pair[1] == 0 {
				if e = db.Exec("TRUNCATE public.target").Error; e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e = my.Exec("TRUNCATE target"); e != nil {
					t.Fatal(e)
				}
			}
			d := workflow.ETLDefinition{Name: "Cross database batch", Source: workflow.TableConfig{SourceID: ids[pair[0]], Schema: schemas[pair[0]], Table: "source"}, Target: workflow.TableConfig{SourceID: ids[pair[1]], Schema: schemas[pair[1]], Table: "target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}, {Source: "amount", Target: "amount", Rule: "copy"}}}
			var task struct {
				ID string `json:"id"`
			}
			if e = json.Unmarshal(post("/api/v1/etl-tasks", d, 201), &task); e != nil {
				t.Fatal(e)
			}
			var run struct {
				ID string `json:"id"`
			}
			if e = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, 201), &run); e != nil {
				t.Fatal(e)
			}
			done := false
			for i := 0; i < 200; i++ {
				var r workflow.Record
				if e = db.Where("kind='etl_run' AND id=?", run.ID).Take(&r).Error; e != nil {
					t.Fatal(e)
				}
				if r.State == "succeeded" {
					done = true
					break
				}
				if r.State == "failed" || r.State == "unknown" {
					var m workflow.ETLRun
					_ = json.Unmarshal(r.Body, &m)
					t.Fatalf("cross database failed %s %s", r.State, m.ErrorCode)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !done {
				t.Fatal("cross exchange timed out")
			}
			var count, bad int64
			if pair[1] == 0 {
				e = db.Raw("SELECT COUNT(*),COUNT(*) FILTER(WHERE name IS DISTINCT FROM 'Alpha' OR amount IS DISTINCT FROM 1234.567890 OR id<9007199254740994 OR id>9007199254742494) FROM public.target").Row().Scan(&count, &bad)
			} else {
				e = my.QueryRow("SELECT COUNT(*),SUM(NOT(name<=>'Alpha') OR NOT(amount<=>1234.567890) OR id<9007199254740994 OR id>9007199254742494) FROM target").Scan(&count, &bad)
			}
			if e != nil || count != 1501 || bad != 0 {
				t.Fatal("precision or batch mismatch", e, count, bad)
			}
		})
	}
	t.Run("database_key_equality", func(t *testing.T) {
		for _, q := range []string{"CREATE TABLE public.key_source(id varchar(5) PRIMARY KEY,name text)", "CREATE TABLE public.key_target(id char(5) PRIMARY KEY,name text)", "INSERT INTO public.key_source VALUES ('a','first'),('a ','last')"} {
			if e := db.Exec(q).Error; e != nil {
				t.Fatal(e)
			}
		}
		d := workflow.ETLDefinition{Name: "Database key equality", Source: workflow.TableConfig{SourceID: ids[0], Schema: "public", Table: "key_source"}, Target: workflow.TableConfig{SourceID: ids[0], Schema: "public", Table: "key_target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "copy"}}}
		var task struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(post("/api/v1/etl-tasks", d, 201), &task)
		var run struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, 201), &run)
		for i := 0; i < 100; i++ {
			var r workflow.Record
			if e := db.Where("kind='etl_run' AND id=?", run.ID).Take(&r).Error; e != nil {
				t.Fatal(e)
			}
			if r.State == "succeeded" {
				var name string
				var count int64
				if e := db.Raw("SELECT COUNT(*),MAX(name) FROM public.key_target").Row().Scan(&count, &name); e != nil || count != 1 || name != "last" {
					t.Fatal("key equality changed data", e, count, name)
				}
				return
			}
			if r.State == "failed" || r.State == "unknown" {
				t.Fatal("batch broke database key equality", r.State)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("key equality run timed out")
	})

	t.Run("case_equivalent_final_key", func(t *testing.T) {
		if e = db.Exec("CREATE TABLE public.case_source(id varchar(5) PRIMARY KEY,name text);INSERT INTO public.case_source VALUES('A','first'),('a','last')").Error; e != nil {
			t.Fatal(e)
		}
		if _, e = my.Exec("CREATE TABLE case_target(id varchar(5) COLLATE utf8mb4_0900_ai_ci PRIMARY KEY,name text) ENGINE=InnoDB"); e != nil {
			t.Fatal(e)
		}
		definition := workflow.ETLDefinition{Name: "Native collation final key", Source: workflow.TableConfig{SourceID: ids[0], Schema: "public", Table: "case_source"}, Target: workflow.TableConfig{SourceID: ids[1], Schema: myDB, Table: "case_target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "copy"}}}
		var task, run struct {
			ID string `json:"id"`
		}
		if e = json.Unmarshal(post("/api/v1/etl-tasks", definition, 201), &task); e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, 201), &run); e != nil {
			t.Fatal(e)
		}
		var page struct {
			Latest *workflow.ETLReconciliation `json:"latest"`
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			w := workflowRequest(router, "GET", "/api/v1/etl-runs/"+run.ID+"/reconciliation", nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatalf("collation report HTTP %d", w.Code)
			}
			if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil {
				t.Fatal(e)
			}
			if page.Latest != nil && (page.Latest.State == "passed" || page.Latest.State == "failed" || page.Latest.State == "mismatched") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if page.Latest == nil || page.Latest.State != "passed" || page.Latest.KeyCount != 1 || page.Latest.CheckedRows != 1 || page.Latest.MismatchCount != 0 {
			t.Fatalf("native collation falsely reconciled: %+v", page.Latest)
		}
		var count int
		var value string
		if e = my.QueryRow("SELECT count(*),max(name) FROM case_target").Scan(&count, &value); e != nil || count != 1 || value != "last" {
			t.Fatalf("collation final values: rows=%d name=%s err=%v", count, value, e)
		}
	})

}
