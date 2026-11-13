package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"
)

func (s *Server) governanceStreamSources(tx *gorm.DB, p identity.Principal, def governanceFlowExecution) (map[string]governanceStreamSource, error) {
	sources := map[string]governanceStreamSource{}
	for node, input := range def.Inputs {
		record, target, e := metadataTarget(tx, p, input.Resource.ID, false)
		if e != nil {
			return nil, e
		}
		if target.SourceID == "" {
			return nil, problem(409, "STREAM_CDC_REQUIRED", "持续更新需要已完成首次采集的数据库 CDC 来源")
		}
		var bindings []CDCBinding
		bindingQuery := tx.Where("source_id=?", target.SourceID)
		if record.Kind == "table_resource" {
			parts := strings.SplitN(target.QualifiedName, ".", 2)
			if len(parts) != 2 {
				return nil, invalid
			}
			bindingQuery = bindingQuery.Where("\"schema\"=? AND \"table\"=?", parts[0], parts[1])
		} else {
			bindingQuery = bindingQuery.Where("object_id=?", record.ID)
		}
		if e = bindingQuery.Find(&bindings).Error; e != nil {
			return nil, e
		}
		if len(bindings) != 1 {
			return nil, problem(409, "STREAM_CDC_REQUIRED", "请在共享交换为每张来源表配置唯一的 CDC 采集任务")
		}
		binding := bindings[0]
		task, e := get(tx, "task", binding.TaskID, false)
		if e != nil {
			return nil, e
		}
		boundary, e := s.cdcSnapshotBoundary(tx.Statement.Context, task)
		if e != nil {
			return nil, e
		}
		if task.State != "streaming" || boundary.State != "complete" {
			return nil, problem(409, "STREAM_SNAPSHOT_PENDING", "请先完成来源 CDC 首次全量采集并保持采集运行")
		}
		ranges, e := cdcTopicRanges(tx.Statement.Context, binding.Topic)
		if e != nil {
			return nil, e
		}
		if len(ranges) != 1 || ranges[0].Start != 0 {
			return nil, problem(409, "STREAM_HISTORY_EXPIRED", "首次启动需要完整的单分区采集历史，请重新建立来源快照")
		}
		for _, field := range input.Fields {
			if field.Sensitive && !admin(p) {
				return nil, problem(403, "STREAM_SENSITIVE_SOURCE", "当前来源含敏感字段，请先移除未获原文读取权限的字段")
			}
		}
		sources[node] = governanceStreamSource{Binding: binding, Fields: input.Fields, SourceFingerprint: binding.SourceFingerprint}
	}
	return sources, nil
}
func streamCreateTopic(ctx context.Context, topic string) error {
	conn, e := kafka.DialContext(ctx, "tcp", "kafka:9092")
	if e != nil {
		return e
	}
	defer conn.Close()
	controller, e := conn.Controller()
	if e != nil {
		return e
	}
	leader, e := kafka.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", controller.Host, controller.Port))
	if e != nil {
		return e
	}
	defer leader.Close()
	_ = leader.SetDeadline(time.Now().Add(5 * time.Second))
	return leader.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1, ConfigEntries: []kafka.ConfigEntry{{ConfigName: "cleanup.policy", ConfigValue: "delete"}, {ConfigName: "retention.ms", ConfigValue: "-1"}}})
}

// At-least-once forwarding uses original offsets. Flink's latest-offset view
// ignores duplicate/replayed changes, including tombstones, before any joins.
func (s *Server) streamForward(ctx context.Context, row *GovernanceStream, sources map[string]governanceStreamSource) error {
	positions, e := decode[map[string]int64](row.Positions)
	if e != nil {
		return e
	}
	if positions == nil {
		positions = map[string]int64{}
	}
	db, e := dorisOpen(ctx, true)
	if e != nil {
		return e
	}
	defer db.Close()
	for node, source := range sources {
		var current CDCBinding
		if e = s.db.WithContext(ctx).Where("task_id=?", source.Binding.TaskID).Take(&current).Error; e != nil {
			return e
		}
		if current.SourceFingerprint != source.SourceFingerprint || current.ConfigVersion != source.Binding.ConfigVersion || current.Topic != source.Binding.Topic {
			return problem(409, "STREAM_SOURCE_CHANGED", "来源采集配置已变化，请核对后新建持续任务")
		}
		ranges, e := cdcTopicRanges(ctx, current.Topic)
		if e != nil {
			return e
		}
		if len(ranges) != 1 || positions[node] < ranges[0].Start {
			return problem(409, "STREAM_HISTORY_EXPIRED", "来源日志已过期，持续任务需要重新建立快照")
		}
		r := ranges[0]
		r.Start = positions[node]
		writer := &kafka.Writer{Addr: kafka.TCP("kafka:9092"), Topic: streamTopic(row.ID, node), RequiredAcks: kafka.RequireAll, Balancer: &kafka.Hash{}, WriteTimeout: 10 * time.Second}
		messages := []kafka.Message{}
		odsArgs := []interface{}{}
		odsMarks := []string{}
		next, err := cdcReadRange(ctx, r, 250, func(msg kafka.Message) error {
			if len(msg.Value) == 0 {
				return nil
			}
			event, e := decodeCaptureEvent(msg.Value)
			if e != nil {
				return e
			}
			key, e := decodeCaptureKey(msg.Key)
			if e != nil {
				return e
			}
			if event.Op != "r" && event.Op != "c" && event.Op != "u" && event.Op != "d" {
				return problem(409, "STREAM_EVENT_UNSUPPORTED", "来源变更事件类型不受支持")
			}
			keyID := fingerprint(encode(key))
			projected := DataRow{}
			body := map[string]interface{}{"_dm_key": keyID, "_dm_seq": msg.Offset, "_dm_deleted": event.Op == "d"}
			for _, f := range source.Fields {
				raw := event.After[f.Name]
				if len(raw) == 0 && event.Op != "d" {
					return problem(409, "STREAM_SOURCE_CHANGED", "来源事件缺少已选字段，请核对结构后重新建立任务")
				}
				if len(raw) == 0 {
					raw = json.RawMessage("null")
				}
				projected[f.Name] = raw
				if event.Op == "d" || len(raw) == 0 || string(raw) == "null" {
					body[f.Name] = nil
				} else {
					value := flowLiteralText(raw)
					if f.LogicalType == "datetime" {
						stamp, err := governanceTimestamp(value)
						if err != nil || stamp.Nanosecond()%1000 != 0 {
							return problem(409, "STREAM_DATETIME_PRECISION", "持续更新要求时间带时区且精度不超过微秒")
						}
						value = stamp.UTC().Format("2006-01-02T15:04:05.000000")
					}
					body[f.Name] = value
				}
			}
			odsArgs = append(odsArgs, node, keyID, msg.Offset, event.Op == "d", string(encode(projected)))
			odsMarks = append(odsMarks, "(?,?,?,?,?)")
			messages = append(messages, kafka.Message{Key: []byte(keyID), Value: encode(body)})
			return nil
		})
		if err == nil && len(odsMarks) > 0 {
			_, err = db.ExecContext(ctx, "INSERT INTO "+dorisDatabase("ods")+".live_"+row.ID+" (node_id,row_key,source_offset,deleted,payload) VALUES "+strings.Join(odsMarks, ","), odsArgs...)
		}
		if err == nil && len(messages) > 0 {
			err = writer.WriteMessages(ctx, messages...)
		}
		closeErr := writer.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		row.InputEvents += next - positions[node]
		positions[node] = next
	}
	row.Positions = encode(positions)
	return nil
}
func (s *Server) governanceStreamSnapshotRun(tx *gorm.DB, p identity.Principal, row GovernanceStream, def governanceFlowExecution) (int, json.RawMessage, string, error) {
	if row.State != "paused" || row.Savepoint == "" {
		return 0, nil, row.ID, problem(409, "STREAM_PAUSE_REQUIRED", "请先暂停到已提交恢复点，再生成一致的成果快照")
	}
	if e := s.governanceFlowAuthorize(tx.Statement.Context, p, def); e != nil {
		return 0, nil, row.ID, e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, row.ID, e
	}
	def.StreamSnapshot = &governanceStreamSnapshot{ID: row.ID, Offset: int64(row.Generation), Positions: row.Positions}
	run := GovernanceRun{ID: id, WorkspaceID: row.WorkspaceID, DepartmentID: row.DepartmentID, ActorID: row.ActorID, Kind: "dataflow", Definition: encode(def), State: "queued", Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&run).Error; e != nil {
		return 0, nil, row.ID, e
	}
	if e = governanceRunEvent(tx, run, "STREAM_SNAPSHOT_QUEUED"); e != nil {
		return 0, nil, row.ID, e
	}
	return 201, encode(run), id, nil
}

func governanceTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if stamp, e := time.Parse(layout, value); e == nil {
			return stamp, nil
		}
	}
	return time.Time{}, problem(409, "GOVERNANCE_TIME_INVALID", "时间值无法无损转换")
}
