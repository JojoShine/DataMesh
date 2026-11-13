package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
	"github.com/go-sql-driver/mysql"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/segmentio/kafka-go"
	goora "github.com/sijms/go-ora/v3"
)

func TestGovernanceCDCRemoteCapturePostgres(t *testing.T) { testCDCRemoteCapture(t, "postgres") }
func TestCDCRemoteCapturePostgres(t *testing.T)           { testCDCRemoteCapture(t, "postgres") }
func TestCDCRemoteCaptureMySQL(t *testing.T)              { testCDCRemoteCapture(t, "mysql") }
func TestCDCRemoteCaptureOracle(t *testing.T)             { testCDCRemoteCapture(t, "oracle") }
func testCDCRemoteCapture(t *testing.T, kind string) {
	u, e := url.Parse(os.Getenv("CDC_TEST_SOURCE_DSN"))
	if kind == "mysql" {
		u = &url.URL{Scheme: "mysql", Host: "mysql-source:3306", Path: "/" + os.Getenv("MYSQL_ETL_TEST_DATABASE"), User: url.UserPassword(os.Getenv("MYSQL_ETL_TEST_USER"), os.Getenv("MYSQL_ETL_TEST_PASSWORD"))}
	}
	if kind == "oracle" {
		u = &url.URL{Scheme: "oracle", Host: "oracle-source:1521", Path: "/FREE", User: url.UserPassword(os.Getenv("ORACLE_TEST_USER"), os.Getenv("ORACLE_TEST_PASSWORD"))}
	}
	if e != nil || kind != "oracle" && !strings.HasPrefix(u.Path, "/datamesh_test_") {
		t.Fatal("isolated remote source required")
	}
	driver, dsn := "pgx", u.String()
	if kind == "mysql" {
		cfg := mysql.NewConfig()
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
		cfg.Net = "tcp"
		cfg.Addr = u.Host
		cfg.DBName = strings.TrimPrefix(u.Path, "/")
		cfg.MultiStatements = true
		cfg.Timeout = 5 * time.Second
		driver = "mysql"
		dsn = cfg.FormatDSN()
	}
	var source *sql.DB
	if kind == "oracle" {
		probe := *u
		probe.Path = "/FREEPDB1"
		q := probe.Query()
		q.Set("FAST LOGIN", "false")
		q.Set("CONNECT TIMEOUT", "5")
		probe.RawQuery = q.Encode()
		source = sql.OpenDB(goora.NewConnector(probe.String()))
	} else {
		source, e = sql.Open(driver, dsn)
	}
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	timeout := 3 * time.Minute
	if strings.HasPrefix(t.Name(), "TestGovernance") {
		timeout = 12 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	names := []string{"cdc_live_a", "cdc_live_b"}
	if kind == "oracle" {
		names = []string{"CDC_LIVE_A", "CDC_LIVE_B"}
	}
	for _, name := range names {
		setup := fmt.Sprintf("CREATE TABLE %s(id bigint PRIMARY KEY,name text); INSERT INTO %s VALUES(1,'initial'),(2,'delete-me');", name, name)
		if kind == "postgres" {
			setup += fmt.Sprintf("CREATE PUBLICATION %s_pub FOR TABLE %s", name, name)
		}
		if kind == "oracle" {
			setup = fmt.Sprintf(`CREATE TABLE %s("id" NUMBER(18,0) PRIMARY KEY,"name" VARCHAR2(80))`, name)
			for _, statement := range []string{setup, fmt.Sprintf(`INSERT INTO %s VALUES(1,'initial')`, name), fmt.Sprintf(`INSERT INTO %s VALUES(2,'delete-me')`, name), fmt.Sprintf(`ALTER TABLE %s ADD SUPPLEMENTAL LOG DATA (ALL) COLUMNS`, name)} {
				if _, e = source.ExecContext(ctx, statement); e != nil {
					t.Fatal("isolated Oracle fixture setup failed")
				}
			}
		} else if _, e = source.ExecContext(ctx, setup); e != nil {
			t.Fatal(e)
		}
	}
	port, _ := strconv.Atoi(u.Port())
	database := strings.TrimPrefix(u.Path, "/")
	password, _ := u.User.Password()
	allowed := []map[string]interface{}{{"kind": kind, "host": u.Hostname(), "port": port, "database": database}}
	if kind == "oracle" {
		allowed = append(allowed, map[string]interface{}{"kind": kind, "host": u.Hostname(), "port": port, "database": "FREEPDB1"})
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, allowed)))
	db, service, router := fixture(t, !strings.HasPrefix(t.Name(), "TestGovernance"))
	var lifecycle *cdcStreamFixture
	if strings.HasPrefix(t.Name(), "TestGovernance") {
		lifecycle = newCDCStreamFixture(t, db, service)
		db, service, router = lifecycle.db, lifecycle.service, lifecycle.router
	}
	_, username := businessUser(t, service, "cdc_live_", "live", []string{"admin"})
	cookie, p := login(t, router, username)
	post := func(path string, value interface{}) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, value), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 200 && w.Code != 201 && w.Code != 202 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	src := post("/api/v1/sources", workflow.SourceInput{Name: "live isolated capture", Kind: kind, Host: u.Hostname(), Port: port, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}})
	bindings := []workflow.CDCBinding{}
	defer func() {
		client := http.Client{Timeout: 8 * time.Second}
		for _, b := range bindings {
			if err := db.Exec("UPDATE cdc_controls SET desired_state='draft',generation=generation+1 WHERE task_id=?", b.TaskID).Error; err != nil {
				t.Error("isolated control cleanup failed")
			}
			for range 40 {
				var ctl workflow.CDCControl
				if err := db.Where("task_id=?", b.TaskID).Take(&ctl).Error; err != nil || ctl.LeaseOwner == "" {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			// Names are generated by this test's own task registrations, never supplied by users.
			req, _ := http.NewRequest("DELETE", "http://connect:8083/connectors/"+url.PathEscape(b.ConnectorName), nil)
			response, err := client.Do(req)
			if err == nil {
				response.Body.Close()
			} else {
				t.Error("isolated connector cleanup failed")
			}
			if kind == "postgres" {
				for range 20 {
					var active bool
					err = source.QueryRow("SELECT active FROM pg_replication_slots WHERE slot_name=$1", b.Slot).Scan(&active)
					if err == sql.ErrNoRows {
						break
					}
					if err == nil && !active {
						_, err = source.Exec("SELECT pg_drop_replication_slot($1)", b.Slot)
						if err != nil {
							t.Error("isolated slot cleanup failed")
						}
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
	}()
	for _, name := range names {
		schema := "public"
		input := map[string]interface{}{"source_id": src["source_id"], "source_version": 1, "schema": schema, "table": name}
		if kind == "postgres" {
			input["publication"] = name + "_pub"
		} else if kind == "oracle" {
			input["schema"] = u.User.Username()
			input["pdb"] = "FREEPDB1"
		} else {
			input["schema"] = database
		}
		registered := post("/api/v1/cdc-tasks", input)
		raw, _ := json.Marshal(registered["capture_binding"])
		var b workflow.CDCBinding
		if e = json.Unmarshal(raw, &b); e != nil {
			t.Fatal(e)
		}
		bindings = append(bindings, b)
		post("/api/v1/tasks/"+b.TaskID+"/start", map[string]interface{}{})
	}
	for _, b := range bindings {
		deadline := time.Now().Add(70 * time.Second)
		complete := false
		for time.Now().Before(deadline) {
			var progress workflow.CDCSnapshotProgress
			if e = db.Where("task_id=?", b.TaskID).Take(&progress).Error; e != nil {
				t.Fatal(e)
			}
			if progress.State == "complete" && progress.ObservedRows == 2 {
				complete = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !complete {
			var ctl workflow.CDCControl
			_ = db.Where("task_id=?", b.TaskID).Take(&ctl).Error
			var progress workflow.CDCSnapshotProgress
			_ = db.Where("task_id=?", b.TaskID).Take(&progress).Error
			t.Fatalf("snapshot not complete: control=%s progress=%s expected=%d observed=%d error=%s", ctl.LastErrorCode, progress.State, progress.ExpectedRows, progress.ObservedRows, progress.ErrorCode)
		}
		tx, err := source.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		idColumn, nameColumn := "id", "name"
		if kind == "oracle" {
			idColumn = `"id"`
			nameColumn = `"name"`
		}
		for _, statement := range []string{fmt.Sprintf("UPDATE %s SET %s='updated' WHERE %s=1", b.Table, nameColumn, idColumn), fmt.Sprintf("INSERT INTO %s VALUES(3,'created')", b.Table), fmt.Sprintf("DELETE FROM %s WHERE %s=2", b.Table, idColumn)} {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				tx.Rollback()
				t.Fatal("isolated source changes failed")
			}
		}
		if err = tx.Commit(); err != nil {
			t.Fatal("isolated source changes commit failed")
		}
		reader := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{"kafka:9092"}, Topic: b.Topic, Partition: 0, StartOffset: kafka.FirstOffset, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: time.Second})
		events := map[string]bool{}
		readCtx, done := context.WithTimeout(ctx, 30*time.Second)
		for len(events) < 3 {
			msg, err := reader.ReadMessage(readCtx)
			if err != nil {
				done()
				reader.Close()
				t.Fatal("live insert/update/delete events missing")
			}
			var event struct {
				Op    string `json:"op"`
				After struct {
					ID   json.Number `json:"id"`
					Name string      `json:"name"`
				} `json:"after"`
				Before struct {
					ID json.Number `json:"id"`
				} `json:"before"`
			}
			eventRaw := msg.Value
			var wrapped struct {
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(msg.Value, &wrapped) == nil && len(wrapped.Payload) > 0 {
				eventRaw = wrapped.Payload
			}
			if e = json.Unmarshal(eventRaw, &event); e != nil {
				t.Fatal(e)
			}
			if event.Op == "u" && event.After.ID.String() == "1" && event.After.Name == "updated" {
				events["u"] = true
			}
			if event.Op == "c" && event.After.ID.String() == "3" && event.After.Name == "created" {
				events["c"] = true
			}
			if event.Op == "d" && event.Before.ID.String() == "2" {
				events["d"] = true
			}
		}
		done()
		reader.Close()
		post("/api/v1/tasks/"+b.TaskID+"/archive", map[string]interface{}{})
		var archive workflow.Record
		deadline = time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if err = db.Where("kind='archive' AND parent_id=?", b.ObjectID).Take(&archive).Error; err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if archive.ID == "" {
			t.Fatal("live CDC archive was not published")
		}
		var meta workflow.ArchiveData
		if err = json.Unmarshal(archive.Body, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.ManifestVersion != 2 || meta.Count != 5 || len(meta.Chunks) == 0 || meta.Boundary.State != "complete" {
			t.Fatalf("unexpected live manifest version=%d events=%d chunks=%d", meta.ManifestVersion, meta.Count, len(meta.Chunks))
		}
		store, err := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{meta.Key}
		for _, chunk := range meta.Chunks {
			keys = append(keys, chunk.Key)
		}
		for _, key := range keys {
			if err = store.RemoveObject(ctx, "datamesh-raw", key, minio.RemoveObjectOptions{}); err != nil {
				t.Error("isolated live archive cleanup failed")
			}
		}
	}
	if lifecycle != nil {
		testCDCStreamLifecycle(t, lifecycle, source, bindings, cookie, p)
	}

	if bindings[0].ConnectorName == bindings[1].ConnectorName || bindings[0].Topic == bindings[1].Topic {
		t.Fatal("two tables shared capture identity")
	}
}
