package workflow

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestArchiveChunkIntegrity(t *testing.T) {
	makeChunk := func(rows []RawRecord) ([]byte, ArchiveChunk) {
		t.Helper()
		var raw, packed bytes.Buffer
		for _, r := range rows {
			if e := json.NewEncoder(&raw).Encode(r); e != nil {
				t.Fatal(e)
			}
		}
		writer := gzip.NewWriter(&packed)
		if _, e := writer.Write(raw.Bytes()); e != nil {
			t.Fatal(e)
		}
		if e := writer.Close(); e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256(packed.Bytes())
		return packed.Bytes(), ArchiveChunk{Key: "business/object/chunk.gz", SHA256: hex.EncodeToString(hash[:]), Partition: 0, Start: 0, End: int64(len(rows)), Count: len(rows), Bytes: int64(raw.Len())}
	}
	rows := []RawRecord{{Topic: "capture.orders", Partition: 0, Offset: 0, Key: json.RawMessage(`{"id":1}`), Event: json.RawMessage(`{"op":"r","after":{"id":1}}`)}, {Topic: "capture.orders", Partition: 0, Offset: 1, Key: json.RawMessage(`{"id":1}`), Event: json.RawMessage(`{"op":"d","after":null}`)}}
	packed, chunk := makeChunk(rows)
	seen := 0
	if e := decodeArchiveChunk(packed, "capture.orders", chunk, 4*1024*1024, func(r RawRecord) error { seen++; return nil }); e != nil || seen != 2 {
		t.Fatalf("read %d: %v", seen, e)
	}
	bad := chunk
	bad.SHA256 = string(bytes.Repeat([]byte("0"), 64))
	if e := decodeArchiveChunk(packed, "capture.orders", bad, 4*1024*1024, func(RawRecord) error { return nil }); e == nil {
		t.Fatal("bad compressed digest accepted")
	}
	bad = chunk
	bad.End = 3
	if e := decodeArchiveChunk(packed, "capture.orders", bad, 4*1024*1024, func(RawRecord) error { return nil }); e == nil {
		t.Fatal("missing event accepted")
	}
	if e := decodeArchiveChunk(packed, "another.table", chunk, 4*1024*1024, func(RawRecord) error { return nil }); e == nil {
		t.Fatal("different topic accepted")
	}
	if e := decodeArchiveChunk(packed, "capture.orders", chunk, 1, func(RawRecord) error { return nil }); e == nil {
		t.Fatal("decompression size cap ignored")
	}
	rows[1].Offset = 9
	packed, chunk = makeChunk(rows)
	if e := decodeArchiveChunk(packed, "capture.orders", chunk, 4*1024*1024, func(RawRecord) error { return nil }); e == nil {
		t.Fatal("gap accepted")
	}
}
func TestArchiveManifestRanges(t *testing.T) {
	meta := ArchiveData{ManifestVersion: 2, ObjectID: "object", Topic: "capture.orders", Count: 2, Boundary: CDCSnapshotBoundary{State: "complete", ObjectVersion: 1, Partitions: []CDCPartitionRange{{Topic: "capture.orders", Partition: 0, Start: 0, End: 2}}}, Chunks: []ArchiveChunk{{Partition: 0, Start: 0, End: 1, Count: 1}, {Partition: 0, Start: 1, End: 2, Count: 1}}}
	if e := validateArchiveManifest(meta); e != nil {
		t.Fatal(e)
	}
	meta.Chunks[1].Start = 0
	if e := validateArchiveManifest(meta); e == nil {
		t.Fatal("overlapping chunks accepted")
	}
	meta.Chunks = nil
	meta.Count = 0
	meta.Boundary.Partitions = nil
	if e := validateArchiveManifest(meta); e != nil {
		t.Fatal("verified empty snapshot rejected")
	}
	meta.Boundary.State = "pending"
	if e := validateArchiveManifest(meta); e == nil {
		t.Fatal("incomplete snapshot accepted")
	}
}
func TestSnapshotCipherScope(t *testing.T) {
	runtime := &Runtime{key: bytes.Repeat([]byte{7}, 32)}
	encrypted, e := runtime.cryptScope([]byte(`{"id":1}`), true, "datamesh-raw-snapshot:one")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := runtime.cryptScope(encrypted, false, "datamesh-raw-snapshot:one")
	if e != nil || string(raw) != `{"id":1}` {
		t.Fatal("snapshot decrypt failed")
	}
	if _, e = runtime.cryptScope(encrypted, false, "datamesh-raw-snapshot:other"); e == nil {
		t.Fatal("ciphertext transferred between snapshots")
	}
	if _, e = runtime.crypt(encrypted, false); e == nil {
		t.Fatal("snapshot ciphertext accepted as source credential")
	}
}

func TestRawSnapshotKeyConsistency(t *testing.T) {
	obj := Object{Fields: []Field{{Name: "id", PrimaryKey: true}, {Name: "name"}}}
	good := RawRecord{Key: json.RawMessage(`{"id":1}`), Event: json.RawMessage(`{"op":"u","after":{"id":1,"name":"updated"}}`)}
	if _, _, err := rawSnapshotEvent(good, obj); err != nil {
		t.Fatal(err)
	}
	good.Event = json.RawMessage(`{"op":"u","after":{"id":2,"name":"wrong-key"}}`)
	if _, _, err := rawSnapshotEvent(good, obj); err == nil {
		t.Fatal("event row key differs from routing key")
	}
}

func TestArchiveSnapshotCoverage(t *testing.T) {
	proof := CDCSnapshotBoundary{State: "complete", Partitions: []CDCPartitionRange{{Topic: "orders", Partition: 0, Start: 0, End: 2}}}
	if e := archiveCoversSnapshot(proof, []CDCPartitionRange{{Topic: "orders", Partition: 0, Start: 0, End: 3}}); e != nil {
		t.Fatal(e)
	}
	for _, ranges := range [][]CDCPartitionRange{nil, {{Topic: "orders", Partition: 0, Start: 0, End: 1}}, {{Topic: "orders", Partition: 0, Start: 1, End: 3}}, {{Topic: "other", Partition: 0, Start: 0, End: 3}}} {
		if e := archiveCoversSnapshot(proof, ranges); e == nil {
			t.Fatal("lost initial range accepted")
		}
	}
}

func TestRawSnapshotLogicalNumbers(t *testing.T) {
	obj := Object{Fields: []Field{{Name: "id", PrimaryKey: true, LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal"}}}
	record := RawRecord{Key: json.RawMessage(`{"id":"9007199254740993"}`), Event: json.RawMessage(`{"op":"r","after":{"id":"9007199254740993","amount":"1.2300"}}`)}
	body, _, e := rawSnapshotEvent(record, obj)
	if e != nil {
		t.Fatal(e)
	}
	if string(body.Key["id"]) != "9007199254740993" || string(body.Row["id"]) != "9007199254740993" || string(body.Row["amount"]) != `"1.2300"` {
		t.Fatal("logical numeric representation not preserved")
	}
}

func TestRawSnapshotRejectsFractionNotation(t *testing.T) {
	obj := Object{Fields: []Field{{Name: "amount", LogicalType: "decimal"}}}
	row := DataRow{"amount": json.RawMessage(`"1/2"`)}
	if e := normalizeSnapshotNumbers(row, obj); e == nil {
		t.Fatal("non-decimal fraction accepted")
	}
}
