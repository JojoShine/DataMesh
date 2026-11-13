package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func cdcTopicRanges(ctx context.Context, topic string) ([]CDCPartitionRange, error) {
	conn, e := kafka.DialContext(ctx, "tcp", "kafka:9092")
	if e != nil {
		return nil, problem(503, "KAFKA_UNAVAILABLE", "无法查询采集事件范围")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	partitions, e := conn.ReadPartitions(topic)
	if e != nil {
		if errors.Is(e, kafka.UnknownTopicOrPartition) {
			return nil, problem(409, "CDC_TOPIC_PENDING", "采集主题尚未就绪")
		}
		return nil, problem(503, "KAFKA_UNAVAILABLE", "无法核查采集主题范围")
	}
	out := []CDCPartitionRange{}
	for _, part := range partitions {
		leader, err := kafka.DialLeader(ctx, "tcp", "kafka:9092", topic, part.ID)
		if err != nil {
			return nil, err
		}
		_ = leader.SetDeadline(time.Now().Add(5 * time.Second))
		start, end, err := leader.ReadOffsets()
		leader.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, CDCPartitionRange{Topic: topic, Partition: part.ID, Start: start, End: end})
	}
	return out, nil
}

func cdcReadRange(ctx context.Context, r CDCPartitionRange, limit int, consume func(kafka.Message) error) (int64, error) {
	if r.Start == r.End {
		return r.End, nil
	}
	reader := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{"kafka:9092"}, Topic: r.Topic, Partition: r.Partition, MinBytes: 1, MaxBytes: 4 * 1024 * 1024, QueueCapacity: 1, MaxWait: time.Second, StartOffset: r.Start, OffsetOutOfRangeError: true})
	defer reader.Close()
	if e := reader.SetOffset(r.Start); e != nil {
		return r.Start, e
	}
	next := r.Start
	for n := 0; next < r.End && n < limit; n++ {
		msg, e := reader.ReadMessage(ctx)
		if e != nil {
			return next, e
		}
		if msg.Offset != next {
			return next, problem(409, "CDC_RANGE_EXPIRED", "采集事件位点不连续，请核查日志保留范围")
		}
		if e = consume(msg); e != nil {
			return next, e
		}
		next++
	}
	return next, nil
}

func (s *Server) cdcObserveSnapshot(parent context.Context, taskID string) error {
	token, e := newID()
	if e != nil {
		return e
	}
	var p CDCSnapshotProgress
	claimed := false
	e = s.db.WithContext(parent).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id=?", taskID).Take(&p).Error; e != nil {
			return e
		}
		if p.State == "complete" || p.ErrorCode != "" || p.LeaseExpiresAt.After(time.Now()) {
			return nil
		}
		p.LeaseOwner = token
		p.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		if e := tx.Save(&p).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if e != nil || !claimed {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	var binding CDCBinding
	observationErr := s.db.WithContext(ctx).Where("task_id=?", taskID).Take(&binding).Error
	if observationErr == nil {
		notificationRanges, err := cdcTopicRanges(ctx, binding.TopicPrefix+".notifications")
		if err == nil && len(notificationRanges) == 1 {
			r := notificationRanges[0]
			if r.Start > p.NotificationOffset {
				p.ErrorCode = "CDC_NOTIFICATION_EXPIRED"
			} else {
				r.Start = p.NotificationOffset
				next, err := cdcReadRange(ctx, r, 1000, func(msg kafka.Message) error { return applyCDCNotification(&p, binding, msg.Value) })
				if err == nil {
					p.NotificationOffset = next
				} else {
					observationErr = err
				}
			}
		} else if err == nil {
			p.ErrorCode = "CDC_NOTIFICATION_PARTITIONS"
		}
		// Notifications are kept in one partition; data ranges remain explicit per partition.
		ranges, err := cdcTopicRanges(ctx, binding.Topic)
		if err == nil {
			var previous []CDCPartitionRange
			if len(p.Partitions) > 0 {
				if e = json.Unmarshal(p.Partitions, &previous); e != nil {
					observationErr = e
				}
			}
			saved := append([]CDCPartitionRange{}, previous...)
			for _, r := range ranges {
				next := int64(0)
				for _, old := range previous {
					if old.Partition == r.Partition {
						next = old.End
					}
				}
				if r.Start > next {
					p.ErrorCode = "CDC_RANGE_EXPIRED"
					break
				}
				if r.End < next {
					p.ErrorCode = "CDC_RANGE_TRUNCATED"
					break
				}
				r.Start = next
				oldObserved := p.ObservedRows
				end, err := cdcReadRange(ctx, r, 20000, func(msg kafka.Message) error {
					var envelope struct {
						Op     string `json:"op"`
						Source struct {
							DB     string `json:"db"`
							Schema string `json:"schema"`
							Table  string `json:"table"`
						} `json:"source"`
					}
					payload, _, err := capturePayload(msg.Value)
					if err != nil {
						return err
					}
					if json.Unmarshal(payload, &envelope) != nil || envelope.Source.Table != binding.Table {
						return problem(409, "CDC_EVENT_INVALID", "采集事件与任务绑定不一致")
					}
					schema := envelope.Source.Schema
					if binding.Kind == "mysql" {
						schema = envelope.Source.DB
					}
					if schema != binding.Schema {
						return problem(409, "CDC_EVENT_INVALID", "采集事件库或 schema 与任务不一致")
					}
					switch envelope.Op {
					case "r":
						p.ObservedRows++
					case "c", "u", "d":
					default:
						return problem(409, "CDC_EVENT_UNSUPPORTED", "采集事件类型尚未通过核查")
					}
					return nil
				})
				// A partial timed-out read is replayed from the previous saved boundary.
				if err != nil {
					p.ObservedRows = oldObserved
					observationErr = err
					break
				}
				updated := CDCPartitionRange{Topic: r.Topic, Partition: r.Partition, Start: 0, End: end}
				replaced := false
				for i, old := range saved {
					if old.Partition == r.Partition {
						saved[i] = updated
						replaced = true
						break
					}
				}
				if !replaced {
					saved = append(saved, updated)
				}
			}
			p.Partitions = encode(saved)
		} else if p.Completed && p.ExpectedRows == 0 {
			p.Partitions = encode([]CDCPartitionRange{})
		}
	}
	if observationErr != nil {
		code := captureErrorCode(observationErr)
		if code == "CDC_RANGE_EXPIRED" || code == "CDC_EVENT_INVALID" || code == "CDC_EVENT_UNSUPPORTED" {
			p.ErrorCode = code
		}
	}
	p.State = cdcSnapshotState(p)
	finish, done := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer done()
	return s.db.WithContext(finish).Model(&CDCSnapshotProgress{}).Where("task_id=? AND lease_owner=?", taskID, token).Updates(map[string]interface{}{"state": p.State, "expected_rows": p.ExpectedRows, "observed_rows": p.ObservedRows, "completed": p.Completed, "error_code": p.ErrorCode, "notification_offset": p.NotificationOffset, "partitions": p.Partitions, "lease_owner": "", "lease_expires_at": time.Now().Add(2 * time.Second), "updated_at": time.Now()}).Error
}

func (s *Server) cdcSnapshotBoundary(ctx context.Context, task Record) (CDCSnapshotBoundary, error) {
	var p CDCSnapshotProgress
	if e := s.db.WithContext(ctx).Where("task_id=?", task.ID).Take(&p).Error; e != nil {
		return CDCSnapshotBoundary{}, e
	}
	data, e := decode[TaskData](task.Body)
	if e != nil {
		return CDCSnapshotBoundary{}, e
	}
	ranges := []CDCPartitionRange{}
	if len(p.Partitions) > 0 {
		if e = json.Unmarshal(p.Partitions, &ranges); e != nil {
			return CDCSnapshotBoundary{}, e
		}
	}
	return CDCSnapshotBoundary{State: p.State, ObjectVersion: data.SchemaVersion, Partitions: ranges}, nil
}

func (s *Server) StartCDCSnapshotWorker(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var rows []Record
				if e := s.db.WithContext(ctx).Where("kind='task' AND state IN ('streaming','paused') AND body->'capture_binding' IS NOT NULL").Find(&rows).Error; e != nil {
					continue
				}
				for _, r := range rows {
					if ctx.Err() != nil {
						return
					}
					if e := s.cdcObserveSnapshot(ctx, r.ID); e != nil && ctx.Err() == nil {
						slog.Error("CDC snapshot observation unavailable", "task_id", r.ID)
					}
				}
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
