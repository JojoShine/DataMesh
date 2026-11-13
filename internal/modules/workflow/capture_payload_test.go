package workflow

import "testing"

func TestCaptureTemporalPrecision(t *testing.T) {
	raw := []byte(`{"schema":{"type":"struct","fields":[{"field":"after","type":"struct","fields":[{"field":"created_at","type":"int64","name":"io.debezium.time.MicroTimestamp"},{"field":"precise_at","type":"int64","name":"io.debezium.time.NanoTimestamp"},{"field":"day","type":"int32","name":"io.debezium.time.Date"},{"field":"amount","type":"string"}]}]},"payload":{"op":"r","after":{"created_at":1704067200123456,"precise_at":1704067200123456789,"day":19723,"amount":"1234.567890"}}}`)
	event, e := decodeCaptureEvent(raw)
	if e != nil {
		t.Fatal(e)
	}
	for key, want := range map[string]string{"created_at": `"2024-01-01 00:00:00.123456"`, "precise_at": `"2024-01-01 00:00:00.123456789"`, "day": `"2024-01-01"`, "amount": `"1234.567890"`} {
		if string(event.After[key]) != want {
			t.Fatalf("%s changed: %s", key, event.After[key])
		}
	}
	key, e := decodeCaptureKey([]byte(`{"schema":{"type":"struct","fields":[{"field":"id","type":"int64"}]},"payload":{"id":9007199254740993}}`))
	if e != nil || string(key["id"]) != "9007199254740993" {
		t.Fatal("exact key lost")
	}
	legacy, e := decodeCaptureEvent([]byte(`{"op":"u","after":{"id":1}}`))
	if e != nil || legacy.Op != "u" {
		t.Fatal("legacy envelope rejected")
	}
}
