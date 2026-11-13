package workflow

import (
	"github.com/segmentio/kafka-go/protocol"
	fetchAPI "github.com/segmentio/kafka-go/protocol/fetch"
	"testing"
)

func TestGovernanceCommittedTransactions(t *testing.T) {
	for _, start := range []int64{0, 3} {
		t.Run(string(rune('0'+start)), func(t *testing.T) {
			batch := func(producer, offset int64) *protocol.RecordBatch {
				return &protocol.RecordBatch{ProducerID: producer, Attributes: protocol.Transactional, Records: protocol.NewRecordReader(protocol.Record{Offset: offset, Key: protocol.NewBytes([]byte("key")), Value: protocol.NewBytes([]byte("value"))})}
			}
			marker := func(producer, offset int64, kind int16) *protocol.ControlBatch {
				r := protocol.NewControlBatch(protocol.ControlRecord{Offset: offset, Type: kind})
				r.ProducerID = producer
				return r
			}
			stream := &protocol.RecordStream{Records: []protocol.RecordReader{batch(7, 0), marker(7, 1, 1), batch(7, 2), batch(7, 3), marker(7, 4, 0), batch(7, 5), marker(7, 6, 1), batch(7, 7), marker(7, 8, 0), batch(8, 9)}}
			rows, next, err := streamFilterCommitted(stream, []fetchAPI.ResponseTransaction{{ProducerID: 7, FirstOffset: 2}, {ProducerID: 7, FirstOffset: 7}}, start, 9)
			if err != nil || next != 9 {
				t.Fatalf("cursor=%d err=%v", next, err)
			}
			want := []int64{0, 5}
			if start == 3 {
				want = []int64{5}
			}
			if len(rows) != len(want) {
				t.Fatalf("rows=%v", rows)
			}
			for i, row := range rows {
				if row.Offset != want[i] {
					t.Fatalf("aborted or uncommitted record exposed: %d", row.Offset)
				}
			}
		})
	}
}
