package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestInspectLightweightCSVParity(t *testing.T) {
	for _, data := range []string{"a,b\n001,\n", "\xef\xbb\xbf编号,name\r\n001,\"a\r\nb\"\r\n", "a\n", "a,b\n\"\",\"\"\n", "\na\n", "a,a\n1,2\n", "a,b\n1\n", "a,b\n\n", "a\n\x00\n", "a\n\xff\n", strings.Repeat("a,", 100) + "z\n"} {
		want, _, err := parseLightweightCSV("test.csv", []byte(data))
		got, e := inspectLightweightCSV("test.csv", []byte(data))
		if (err == nil) != (e == nil) {
			t.Fatalf("error mismatch %q: %v / %v", data, err, e)
		}
		if err != nil {
			if err.Error() != e.Error() {
				t.Fatalf("error mismatch: %v / %v", err, e)
			}
			continue
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("metadata mismatch for %q", data)
		}
	}
}
func TestInspectLightweightCSVAllocation(t *testing.T) {
	data := []byte("a,b,c,d,e,f,g,h,i,j\n" + strings.Repeat("1,2,3,4,5,6,7,8,9,0\n", 10000))
	inspect := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := inspectLightweightCSV("large.csv", data); err != nil {
				b.Fatal(err)
			}
		}
	})
	collect := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := parseLightweightCSV("large.csv", data); err != nil {
				b.Fatal(err)
			}
		}
	})
	if inspect.AllocedBytesPerOp()*2 >= collect.AllocedBytesPerOp() {
		t.Fatalf("metadata retained excessive allocations: inspect=%d collect=%d", inspect.AllocedBytesPerOp(), collect.AllocedBytesPerOp())
	}
	t.Logf("metadata bytes/op=%d, collecting bytes/op=%d", inspect.AllocedBytesPerOp(), collect.AllocedBytesPerOp())
}
