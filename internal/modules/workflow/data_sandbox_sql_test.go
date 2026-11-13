package workflow

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSandboxSQL(t *testing.T) {
	tables := map[string]sandboxSQLTable{"t": {Database: "safe", Table: "physical", Fields: []Field{{Name: "id"}, {Name: "金额"}}}}
	for _, sql := range []string{"select * from t", "select t.id,cast(t.id as decimal(20,2)) from t", "with a as (select * from t), b as (select * from a) select * from b", "select case when id='drop table t' then 'ok' else 'no' end as `中文` from t", "select count(*) from t group by id having count(*)>0", "select * from (select * from t) q join t on q.id=t.id", "select * from t union all select * from t"} {
		t.Run(sql, func(t *testing.T) {
			got, err := compileSandboxSQL(sql, tables)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "`safe`.`physical`") {
				t.Fatal(got)
			}
		})
	}
	for _, sql := range []string{"delete from t", "select * from other", "select * from safe.t", "select sleep(1)", "select @@version", "select ?", "select 1;select 2", "select * from t for update", "select * into outfile '/tmp/x' from t", "select /*+ MAX_EXECUTION_TIME(1) */ * from t", "select /*!50000 sleep(1) */ 1", "select other.abs(1)", "with recursive a as(select * from a) select * from a", "with t as(select 1) select * from t", "with a as(select * from b),b as(select * from t) select * from a"} {
		t.Run(sql, func(t *testing.T) {
			if got, err := compileSandboxSQL(sql, tables); err == nil {
				t.Fatalf("accepted %s", got)
			}
		})
	}
}

func TestSandboxSQLDorisLiteralRestore(t *testing.T) {
	got, err := compileSandboxSQL("WITH picked AS (SELECT id FROM t WHERE id = '0012') SELECT id FROM picked", map[string]sandboxSQLTable{"t": {Database: "safe", Table: "physical", Fields: []Field{{Name: "id"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(got), "_UTF8") {
		t.Fatalf("Doris cannot parse TiDB charset introducer: %s", got)
	}
	if !strings.Contains(got, "'0012'") {
		t.Fatal(got)
	}
}

func TestSandboxSQLActualDorisPrecisionCastCase(t *testing.T) {
	if os.Getenv("SANDBOX_DORIS_TEST") != "1" {
		t.Skip("requires sandbox-only Doris credentials")
	}
	sql, err := compileSandboxSQL("SELECT CAST('9223372036854775807' AS SIGNED) AS bigvalue,CAST('9007199254740993.123456' AS DECIMAL(30,6)) AS amount,CASE WHEN 1=1 THEN NULL ELSE '' END AS optional,CASE WHEN 1=0 THEN NULL ELSE '' END AS empty_value", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sandboxDorisRead(context.Background(), sql, 100, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Values) != 1 || len(got.Values[0]) != 4 || got.Values[0][0] != "9223372036854775807" || got.Values[0][1] != "9007199254740993.123456" || got.Values[0][2] != nil || got.Values[0][3] != "" {
		t.Fatal("precision or NULL/empty distinction changed", got.Values)
	}
	if _, err := compileSandboxSQL("SELECT CAST('1' AS BIGINT)", nil); err == nil {
		t.Fatal("accepted invalid MySQL CAST BIGINT syntax")
	}
}
