package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/protocol"
	fetchAPI "github.com/segmentio/kafka-go/protocol/fetch"
)

// kafka-go's high-level reader does not remove aborted transactional batches.
// Retain the broker's abort metadata and control batches until filtering is done.
func streamCommittedMessages(ctx context.Context, topic string, offset int64) ([]kafka.Message, int64, int64, error) {
	response, err := kafka.DefaultTransport.RoundTrip(ctx, kafka.TCP("kafka:9092"), &fetchAPI.Request{
		ReplicaID: -1, MaxWaitTime: 1000, MinBytes: 1, MaxBytes: 4 << 20,
		IsolationLevel: 1, SessionID: -1, SessionEpoch: -1,
		Topics: []fetchAPI.RequestTopic{{Topic: topic, Partitions: []fetchAPI.RequestPartition{{
			Partition: 0, CurrentLeaderEpoch: -1, FetchOffset: offset, LogStartOffset: -1, PartitionMaxBytes: 4 << 20,
		}}}},
	})
	if err != nil {
		return nil, offset, 0, err
	}
	result, ok := response.(*fetchAPI.Response)
	if !ok || len(result.Topics) != 1 || result.Topics[0].Topic != topic || len(result.Topics[0].Partitions) != 1 {
		return nil, offset, 0, fmt.Errorf("invalid Kafka fetch response")
	}
	if result.ErrorCode != 0 {
		return nil, offset, 0, kafka.Error(result.ErrorCode)
	}
	partition := result.Topics[0].Partitions[0]
	if partition.ErrorCode != 0 {
		return nil, offset, 0, kafka.Error(partition.ErrorCode)
	}
	if offset < partition.LogStartOffset {
		return nil, offset, 0, problem(409, "STREAM_RESULT_HISTORY_EXPIRED", "结果变更日志已过期，不能跳过缺失事件")
	}
	messages, next, err := streamFilterCommitted(partition.RecordSet.Records, partition.AbortedTransactions, offset, partition.LastStableOffset)
	return messages, next, partition.HighWatermark, err
}

func streamFilterCommitted(reader protocol.RecordReader, transactions []fetchAPI.ResponseTransaction, offset, stable int64) ([]kafka.Message, int64, error) {
	if reader == nil {
		return nil, offset, nil
	}
	batches := []protocol.RecordReader{reader}
	if stream, ok := reader.(*protocol.RecordStream); ok {
		batches = stream.Records
	}
	// A response can contain several aborted transactions from the same producer.
	aborted := map[int64][]int64{}
	for _, tx := range transactions {
		aborted[tx.ProducerID] = append(aborted[tx.ProducerID], tx.FirstOffset)
	}
	for _, starts := range aborted {
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	}
	closeRecord := func(record *protocol.Record) {
		if record.Key != nil {
			_ = record.Key.Close()
		}
		if record.Value != nil {
			_ = record.Value.Close()
		}
	}
	defer func() {
		for _, batch := range batches {
			for {
				record, err := batch.ReadRecord()
				if err != nil {
					break
				}
				closeRecord(record)
			}
		}
	}()
	next := offset
	messages := []kafka.Message{}
	for _, batch := range batches {
		var producer int64
		var transactional, control bool
		switch typed := batch.(type) {
		case *protocol.ControlBatch:
			producer, control = typed.ProducerID, true
		case *protocol.RecordBatch:
			producer, transactional = typed.ProducerID, typed.Attributes.Transactional()
		default:
			return nil, offset, fmt.Errorf("unsupported Kafka result batch")
		}
		for {
			record, err := batch.ReadRecord()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, offset, err
			}
			if record.Offset >= stable {
				closeRecord(record)
				continue
			}
			if record.Offset >= next {
				next = record.Offset + 1
			}
			if control {
				marker, err := protocol.ReadControlRecord(record)
				if err != nil {
					return nil, offset, err
				}
				if marker.Version != 0 || (marker.Type != 0 && marker.Type != 1) {
					return nil, offset, fmt.Errorf("unsupported Kafka transaction marker")
				}
				if marker.Type == 0 {
					starts := aborted[producer]
					if len(starts) > 0 && starts[0] <= marker.Offset {
						aborted[producer] = starts[1:]
					}
				}
				continue
			}
			starts := aborted[producer]
			if record.Offset < offset || (transactional && len(starts) > 0 && record.Offset >= starts[0]) {
				closeRecord(record)
				continue
			}
			key, err := protocol.ReadAll(record.Key)
			if err != nil {
				closeRecord(record)
				return nil, offset, err
			}
			value, err := protocol.ReadAll(record.Value)
			closeRecord(record)
			if err != nil {
				return nil, offset, err
			}
			messages = append(messages, kafka.Message{Offset: record.Offset, Key: key, Value: value})
		}
	}
	return messages, next, nil
}
