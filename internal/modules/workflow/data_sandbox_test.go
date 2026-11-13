package workflow

import (
	"datamesh/internal/modules/identity"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSandboxVisitorScopeIsolation(t *testing.T) {
	a := identity.Principal{FullExperience: true, CSRFToken: "session-a"}
	b := a
	b.CSRFToken = "session-b"
	if sandboxVisitorScope(a) == sandboxVisitorScope(b) {
		t.Fatal("sessions share scope")
	}
	if sandboxVisitorScope(a) == a.CSRFToken {
		t.Fatal("raw session secret persisted")
	}
	a.CSRFToken = ""
	if sandboxVisitorScope(a) != "" {
		t.Fatal("missing token must be rejected by owner helper")
	}
	a.FullExperience = false
	a.CSRFToken = "ordinary"
	if sandboxVisitorScope(a) != "" {
		t.Fatal("ordinary ownership must persist across sessions")
	}
}
func TestSandboxLogicalNameBoundary(t *testing.T) {
	for _, name := range []string{"a.b", "a`b", "a b", "", "1table"} {
		if sandboxIdentifier.MatchString(name) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"orders", "_source", "table_2"} {
		if !sandboxIdentifier.MatchString(name) {
			t.Errorf("rejected %q", name)
		}
	}
}

func TestSandboxPublicationFence(t *testing.T) {
	version := 3
	box := DataSandbox{State: "active", Version: version, ExpiresAt: time.Now().Add(time.Hour)}
	if !sandboxBoxValid(box, &version) {
		t.Fatal("valid box rejected")
	}
	if sandboxBoxValid(box, nil) {
		t.Fatal("missing frozen version accepted")
	}
	old := 2
	if sandboxBoxValid(box, &old) {
		t.Fatal("stale version accepted")
	}
	box.State = "closing"
	if sandboxBoxValid(box, &version) {
		t.Fatal("closing box accepted")
	}
	box.State = "active"
	box.ExpiresAt = time.Now().Add(-time.Second)
	if sandboxBoxValid(box, &version) {
		t.Fatal("expired box accepted")
	}
}

func TestSandboxCanonicalRejectsUnreadableValuesBeforeStorage(t *testing.T) {
	server := &Server{}
	for _, rows := range []sandboxRows{
		{Fields: []Field{{Name: "value"}}, Values: [][]interface{}{{strings.Repeat("x", (1<<20)+1)}}},
		{Fields: []Field{{Name: "value"}}, Values: [][]interface{}{{true}}},
		{Fields: []Field{{Name: "value"}}, Values: [][]interface{}{{"a", "b"}}},
		{Fields: []Field{}, Values: [][]interface{}{}},
	} {
		if _, err := server.sandboxStoreObject(t.Context(), rows); err == nil {
			t.Fatal("unreadable canonical object reached storage")
		}
	}
	_, err := server.sandboxStoreObject(t.Context(), sandboxRows{Fields: []Field{{Name: "value"}}, Values: [][]interface{}{{strings.Repeat("x", (1<<20)+1)}}})
	var issue *Problem
	if !errors.As(err, &issue) || issue.Code != "SANDBOX_CELL_LIMIT" {
		t.Fatalf("expected cell limit: %v", err)
	}
}

func TestSandboxCanonicalExpansionAndCSVLimit(t *testing.T) {
	fields := make([]Field, 100)
	record := make([]interface{}, 100)
	for i := range fields {
		fields[i] = Field{Name: fmt.Sprintf("column_%d", i)}
		record[i] = ""
	}
	rows := sandboxRows{Fields: fields, Values: make([][]interface{}, 100000)}
	for i := range rows.Values {
		rows.Values[i] = record
	}
	data, err := sandboxEncodeObject(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= lightweightMaxBytes || len(data) > sandboxCanonicalMaxBytes {
		t.Fatalf("unexpected canonical expansion: %d", len(data))
	}
	csvSize, err := sandboxCSVSize(rows)
	if err != nil || csvSize > lightweightMaxBytes {
		t.Fatalf("valid CSV rejected: size=%d error=%v", csvSize, err)
	}
	empty := sandboxRows{Fields: fields, Values: [][]interface{}{}}
	if _, err = sandboxEncodeObject(empty); err != nil {
		t.Fatalf("zero rows rejected: %v", err)
	}
	tooBig := sandboxRows{Fields: []Field{{Name: "value"}}, Values: make([][]interface{}, 21)}
	for i := range tooBig.Values {
		tooBig.Values[i] = []interface{}{strings.Repeat("x", 1<<20)}
	}
	if _, err = sandboxCSVSize(tooBig); err == nil {
		t.Fatal("CSV larger than20MiB accepted")
	}
	tooBig.Values = make([][]interface{}, 65)
	large := []interface{}{strings.Repeat("x", 1<<20)}
	for i := range tooBig.Values {
		tooBig.Values[i] = large
	}
	if _, err = sandboxEncodeObject(tooBig); err == nil {
		t.Fatal("internal canonical larger than64MiB accepted")
	}
}
