package webpath

import "testing"

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{"": "", "/": "", "/datamesh": "/datamesh", "/datamesh/": "/datamesh", "/apps/data-mesh": "/apps/data-mesh"} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q)=%q,%v", input, got, err)
		}
	}
	for _, input := range []string{"//", "//datamesh", "/datamesh//", "/../other", "/x%2fy", "/x?y=1", "/x#z", "https://example.com/x", "/x\r\nPath=/"} {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("accepted unsafe prefix %q", input)
		}
	}
}
