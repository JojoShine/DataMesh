package migrations

import "testing"

func TestVersion(t *testing.T) {
	version, err := Version()
	if err != nil || version != 48 {
		t.Fatalf("unexpected release schema: %d %v", version, err)
	}
}
