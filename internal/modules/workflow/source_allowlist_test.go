package workflow

import "testing"

func TestSourceTargetValidation(t *testing.T) {
	for _, host := range []string{"db.internal", "postgres-source", "10.0.0.8", "2001:db8::8", "DB.internal."} {
		for _, kind := range []string{"postgres", "mysql", "oracle"} {
			if !validSourceTarget(SourceTarget{Kind: kind, Host: host, Port: 5432, Database: "business"}) {
				t.Fatalf("valid %s %s", kind, host)
			}
		}
	}
	for _, host := range []string{"", "*", "db.*", "db.internal:5432", "https://db.internal", "db.internal/path", " db.internal", "user@db.internal", "db..internal", "-db.internal"} {
		if validSourceTarget(SourceTarget{Kind: "postgres", Host: host, Port: 5432, Database: "business"}) {
			t.Fatalf("invalid host %q", host)
		}
	}
	for _, database := range []string{"", "*", "business\n", "business\x00", " business"} {
		if validSourceTarget(SourceTarget{Kind: "postgres", Host: "db.internal", Port: 5432, Database: database}) {
			t.Fatalf("invalid database %q", database)
		}
	}
}
