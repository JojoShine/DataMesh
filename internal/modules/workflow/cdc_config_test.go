package workflow

import (
	"regexp"
	"testing"
)

func TestCDCConfig(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "oracle"} {
		t.Run(kind, func(t *testing.T) {
			src := SourceSecret{Kind: kind, Host: "remote-db", Port: 5432, Database: "business"}
			in := CDCConfigInput{SourceID: "source", SourceVersion: 2, Schema: "business", Table: "orders.v2+data"}
			if kind == "postgres" {
				in.Publication = "orders_publication"
			}
			if kind == "oracle" {
				in.PDB = "BUSINESS_PDB"
			}
			b, e := newCDCBinding("0123456789abcdef0123456789abcdef", src, in, "obj", "physical")
			if e != nil {
				t.Fatal(e)
			}
			cfg, e := buildCDCConfig(b, src, Credential{Username: "capture", Password: "only-internal-password"})
			if e != nil {
				t.Fatal(e)
			}
			if cfg["topic.prefix"] != b.TopicPrefix || cfg["database.hostname"] != src.Host || cfg["snapshot.mode"] != "initial" {
				t.Fatal("binding not used")
			}
			tableRE, e := regexp.Compile(cfg["table.include.list"])
			if e != nil {
				t.Fatal(e)
			}
			if !tableRE.MatchString(in.Schema+"."+in.Table) || tableRE.MatchString("prefix"+in.Schema+"."+in.Table) || tableRE.MatchString(in.Schema+".ordersXv2data") {
				t.Fatal("table scope expanded")
			}
			if kind == "postgres" && (cfg["slot.name"] != b.Slot || cfg["publication.name"] != in.Publication || cfg["publication.autocreate.mode"] != "disabled") {
				t.Fatal("publication isolation")
			}
			if kind == "mysql" && (cfg["database.server.id"] == "" || cfg["schema.history.internal.kafka.topic"] != b.SchemaHistoryTopic) {
				t.Fatal("MySQL identity missing")
			}
			if kind == "oracle" {
				if cfg["database.pdb.name"] != "BUSINESS_PDB" {
					t.Fatal("PDB lost")
				}
				if cfg["log.mining.flush.table.name"] != "DMF_0123456789abcdef01234567" {
					t.Fatal("Oracle tasks share LogMiner flush table")
				}
			}
			if cfg["notification.enabled.channels"] != "sink" || cfg["notification.sink.topic.name"] != b.TopicPrefix+".notifications" {
				t.Fatal("snapshot completion channel missing")
			}
		})
	}
}
