package workflow

import (
	"github.com/go-sql-driver/mysql"
	"os"
	"testing"
)

func TestDorisInstanceAddress(t *testing.T) {
	t.Setenv("GOVERNANCE_DORIS_ADDRESS", "")
	if dorisAddress() != "172.29.238.10:9030" {
		t.Fatal("main default changed")
	}
	t.Setenv("GOVERNANCE_DORIS_ADDRESS", "doris-fe:9030")
	if dorisAddress() != "doris-fe:9030" {
		t.Fatal("isolated instance falls back to primary")
	}
}

func TestDorisInstanceNamespace(t *testing.T) {
	t.Setenv("DORIS_DATABASE_PREFIX", "experience_")
	t.Setenv("DORIS_READER_USER", "experience_reader")
	t.Setenv("DORIS_WRITER_USER", "experience_writer")
	t.Setenv("DORIS_QUERY_MEMORY_BYTES", "134217728")
	t.Setenv("DORIS_QUERY_TIMEOUT_SECONDS", "30")
	t.Setenv("DORIS_WORKLOAD_GROUP", "experience_compute")
	if err := validateDorisConfiguration(); err != nil {
		t.Fatal(err)
	}
	if dorisDatabase("dwd") != "experience_dwd" || dorisDatabase("internal") != "experience_internal" || dorisDatabase("smoke") != "experience_smoke" || dorisUser(false) != "experience_reader" || dorisUser(true) != "experience_writer" {
		t.Fatal("Doris configuration crosses instance namespace")
	}
	cfg := mysql.NewConfig()
	if err := configureDorisSession(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Params["exec_mem_limit"] != "134217728" || cfg.Params["query_timeout"] != "30" || cfg.Params["workload_group"] != "'experience_compute'" {
		t.Fatal("query budgets or workload isolation not applied")
	}
	for key, value := range map[string]string{"DORIS_DATABASE_PREFIX": "main;DROP DATABASE datamesh_dwd;", "DORIS_READER_USER": "root@%", "DORIS_QUERY_MEMORY_BYTES": "134217728;SET x=1", "DORIS_QUERY_TIMEOUT_SECONDS": "0", "DORIS_WORKLOAD_GROUP": "main;SET x=1"} {
		previous := os.Getenv(key)
		t.Setenv(key, value)
		if validateDorisConfiguration() == nil {
			t.Fatalf("accepted invalid %s", key)
		}
		t.Setenv(key, previous)
	}
}
