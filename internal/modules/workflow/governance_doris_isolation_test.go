package workflow

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// This privileged fixture is opt-in and creates only randomly named test
// databases/identities. The existing main namespace is checked with SELECT only.
func TestDorisSharedNamespaceIsolation(t *testing.T) {
	if os.Getenv("DATAMESH_DORIS_ISOLATION_TEST") != "1" {
		t.Skip("requires opt-in shared Doris isolation fixture")
	}
	file, err := os.Open("../../../.local/runtime.env")
	if err != nil {
		t.Fatal("local Doris administration fixture unavailable")
	}
	rootPassword := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "DORIS_ROOT_PASSWORD="); ok {
			rootPassword = value
		}
	}
	scanErr := scanner.Err()
	closeErr := file.Close()
	if scanErr != nil || closeErr != nil || !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(rootPassword) {
		t.Fatal("invalid local Doris administration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = rootPassword
	cfg.Net = "tcp"
	cfg.Addr = dorisAddress()
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open Doris administration fixture")
	}
	defer admin.Close()
	admin.SetMaxOpenConns(2)
	if err = admin.PingContext(ctx); err != nil {
		t.Fatal("Doris administration fixture unavailable")
	}
	// Verify the main target exists, so denied reads cannot pass due to a missing table.
	if _, err = admin.ExecContext(ctx, "SELECT 1 FROM datamesh_smoke.customers LIMIT 0"); err != nil {
		t.Fatal("main read-only canary unavailable")
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	suffix := id[:12]
	prefix := "isotest_" + suffix + "_"
	neighbor := "isotest_neighbor_" + suffix + "_dwd"
	reader, writer := "isotest_r_"+suffix, "isotest_w_"+suffix
	databases := []string{neighbor}
	for _, layer := range []string{"internal", "ods", "quality", "dwd", "dim", "dws", "ads", "smoke"} {
		databases = append(databases, prefix+layer)
	}
	createdDatabases := []string{}
	createdUsers := []string{}
	defer func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		for _, database := range createdDatabases {
			if !strings.HasPrefix(database, "isotest_") {
				t.Error("refused unsafe fixture cleanup")
				continue
			}
			if _, err := admin.ExecContext(cleanCtx, "DROP DATABASE IF EXISTS "+dorisIdentifier(database)); err != nil {
				t.Error("isolated Doris database cleanup failed")
			}
		}
		for _, user := range createdUsers {
			if _, err := admin.ExecContext(cleanCtx, "DROP USER IF EXISTS '"+user+"'"); err != nil {
				t.Error("isolated Doris identity cleanup failed")
			}
		}
	}()
	exec := func(query string) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, query); err != nil {
			t.Fatal("isolated Doris provisioning failed")
		}
	}
	for _, database := range databases {
		exec("CREATE DATABASE " + dorisIdentifier(database))
		createdDatabases = append(createdDatabases, database)
	}
	password := func() string {
		t.Helper()
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(raw)
	}
	readerPassword, writerPassword := password(), password()
	exec("CREATE USER '" + reader + "' IDENTIFIED BY '" + readerPassword + "'")
	createdUsers = append(createdUsers, reader)
	exec("CREATE USER '" + writer + "' IDENTIFIED BY '" + writerPassword + "'")
	createdUsers = append(createdUsers, writer)
	for _, database := range databases[1:] {
		exec("GRANT SELECT_PRIV, LOAD_PRIV, CREATE_PRIV, DROP_PRIV, ALTER_PRIV ON " + dorisIdentifier(database) + ".* TO '" + writer + "'")
	}
	for _, layer := range []string{"dwd", "dim", "dws", "ads"} {
		exec("GRANT SELECT_PRIV ON " + dorisIdentifier(prefix+layer) + ".* TO '" + reader + "'")
	}
	for _, database := range []string{neighbor, prefix + "dwd", prefix + "internal"} {
		exec("CREATE TABLE " + dorisIdentifier(database) + ".privilege_probe(id INT NOT NULL) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1')")
		exec("INSERT INTO " + dorisIdentifier(database) + ".privilege_probe VALUES(1)")
	}
	t.Setenv("DORIS_DATABASE_PREFIX", prefix)
	t.Setenv("DORIS_READER_USER", reader)
	t.Setenv("DORIS_WRITER_USER", writer)
	t.Setenv("DORIS_READER_PASSWORD", readerPassword)
	t.Setenv("DORIS_WRITER_PASSWORD", writerPassword)
	readDB, err := dorisOpen(ctx, false)
	if err != nil {
		t.Fatal("isolated reader unavailable")
	}
	defer readDB.Close()
	writeDB, err := dorisOpen(ctx, true)
	if err != nil {
		t.Fatal("isolated writer unavailable")
	}
	defer writeDB.Close()
	denied := func(db *sql.DB, query string) {
		t.Helper()
		_, err := db.ExecContext(ctx, query)
		if err == nil {
			t.Fatal("Doris namespace boundary permitted unauthorized operation")
		}
		var failure *mysql.MySQLError
		if !errors.As(err, &failure) {
			t.Fatal("namespace test failed without a server authorization error")
		}
		lower := strings.ToLower(failure.Message)
		if failure.Number != 1044 && failure.Number != 1045 && failure.Number != 1142 && failure.Number != 1227 && !strings.Contains(lower, "denied") && !strings.Contains(lower, "privilege") {
			t.Fatalf("unexpected Doris error code: %d", failure.Number)
		}
	}
	for _, db := range []*sql.DB{readDB, writeDB} {
		denied(db, "SELECT 1 FROM datamesh_smoke.customers LIMIT 0")
		denied(db, "SELECT id FROM "+dorisIdentifier(neighbor)+".privilege_probe")
		denied(db, "INSERT INTO "+dorisIdentifier(neighbor)+".privilege_probe VALUES(2)")
	}
	denied(readDB, "SELECT id FROM "+dorisIdentifier(prefix+"internal")+".privilege_probe")
	denied(readDB, "INSERT INTO "+dorisIdentifier(prefix+"dwd")+".privilege_probe VALUES(2)")
	if _, err = writeDB.ExecContext(ctx, "INSERT INTO "+dorisIdentifier(prefix+"dwd")+".privilege_probe VALUES(2)"); err != nil {
		t.Fatal("writer could not write own namespace")
	}
	var own, other int
	if err = readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dorisIdentifier(prefix+"dwd")+".privilege_probe").Scan(&own); err != nil || own != 2 {
		t.Fatal("reader could not read own published namespace")
	}
	if err = admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dorisIdentifier(neighbor)+".privilege_probe").Scan(&other); err != nil || other != 1 {
		t.Fatal("neighbor namespace was modified")
	}
	t.Setenv("DATAMESH_DORIS_TEST", "1")
	t.Run("actual_governance_engine", TestDorisGovernanceActualEngine)
}
