package workflow

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestCDCBindingIsolation(t *testing.T) {
	src := SourceSecret{Kind: "postgres", Host: "db.example", Port: 5432, Database: "business", Ciphertext: "secret-never-exposed"}
	cfg := CDCConfigInput{SourceID: "source", Schema: "public", Table: "odd.table+name", SourceVersion: 3, Publication: "business_publication"}
	a, e := newCDCBinding("0123456789abcdef0123456789abcdef", src, cfg, "object", "verified-physical-db")
	if e != nil {
		t.Fatal(e)
	}
	b, e := newCDCBinding("1123456789abcdef0123456789abcdef", src, cfg, "object", "verified-physical-db")
	if e != nil {
		t.Fatal(e)
	}
	if a.ConnectorName == b.ConnectorName || a.TopicPrefix == b.TopicPrefix || a.Slot == b.Slot {
		t.Fatal("capture identities collide")
	}
	if len(a.Slot) > 63 || !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(a.Slot) {
		t.Fatal("invalid slot")
	}
	if a.SourceFingerprint != b.SourceFingerprint {
		t.Fatal("physical alias fingerprint changed with task")
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "secret-never-exposed") {
		t.Fatal("credential leaked")
	}
}
func TestCDCLegacyBinding(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "oracle"} {
		got, e := resolveCDCBinding(Record{ID: "legacy"}, TaskData{Binding: kind})
		if e != nil || got.ConnectorName != "datamesh-"+kind || got.TopicPrefix != "datamesh_"+kind || got.Topic != topicFor(kind) || !got.Legacy {
			t.Fatalf("legacy %s: %+v %v", kind, got, e)
		}
	}
	if _, e := resolveCDCBinding(Record{}, TaskData{Binding: "attacker/../connector"}); e == nil {
		t.Fatal("invalid legacy binding accepted")
	}
}
func TestCDCBindingInput(t *testing.T) {
	for _, cfg := range []CDCConfigInput{
		{SourceID: "s", Schema: "public", Table: "t", SourceVersion: 1},
		{SourceID: "s", Schema: "public", Table: "t\nother", SourceVersion: 1, Publication: "p"},
	} {
		if _, e := newCDCBinding("0123456789abcdef0123456789abcdef", SourceSecret{Kind: "postgres"}, cfg, "o", "verified"); e == nil {
			t.Fatal("unsafe or incomplete publication accepted")
		}
	}
}

func TestCDCOracleProbeScope(t *testing.T) {
	source := SourceSecret{Kind: "oracle", Host: "db.example", Port: 1521, Database: "ROOT_SERVICE", Ciphertext: "encrypted"}
	probe, e := cdcProbeSource(source, "BUSINESS_PDB")
	if e != nil || probe.Database != "BUSINESS_PDB" || source.Database != "ROOT_SERVICE" || probe.Host != source.Host || probe.Ciphertext != source.Ciphertext {
		t.Fatal("Oracle SQL probe must use PDB without altering root capture source")
	}
	if _, e = cdcProbeSource(source, "ROOT_SERVICE"); e == nil {
		t.Fatal("CDB/PDB ambiguity accepted")
	}
	if _, e = cdcProbeSource(SourceSecret{Kind: "postgres"}, "OTHER_PDB"); e == nil {
		t.Fatal("PDB scope accepted for PostgreSQL")
	}
}

func TestCDCControlFailureFinality(t *testing.T) {
	if cdcControlFailureFinal(problem(503, "CAPTURE_UNAVAILABLE", "unavailable")) {
		t.Fatal("response loss treated as definitive rejection")
	}
	if cdcControlFailureFinal(problem(409, "CONTROL_UNKNOWN", "waiting")) {
		t.Fatal("transitional observation treated as definitive rejection")
	}
	if !cdcControlFailureFinal(problem(409, "CDC_CONFIG_CONFLICT", "mismatch")) {
		t.Fatal("configuration rejection never finishes operation")
	}
}

func TestCDCControlPauseConfirmation(t *testing.T) {
	var state ConnectorStatus
	state.Connector.State = "PAUSED"
	state.Tasks = append(state.Tasks, struct {
		State string `json:"state"`
	}{State: "RUNNING"})
	if cdcPaused(state) {
		t.Fatal("running task reported as paused")
	}
	state.Tasks[0].State = "PAUSED"
	if !cdcPaused(state) {
		t.Fatal("confirmed pause rejected")
	}
}
func TestCDCGeneratedTopicLimit(t *testing.T) {
	_, e := newCDCBinding(strings.Repeat("a", 32), SourceSecret{Kind: "oracle"}, CDCConfigInput{SourceID: "source", SourceVersion: 1, Schema: strings.Repeat("S", 128), Table: strings.Repeat("T", 128), PDB: "FREEPDB1"}, "object", "physical")
	if e == nil {
		t.Fatal("generated topic exceeds Kafka's249-byte limit")
	}
}
