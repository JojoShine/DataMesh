package workflow

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"
)

type CDCConfigInput struct {
	SourceID      string `json:"source_id"`
	SourceVersion int    `json:"source_version"`
	Schema        string `json:"schema"`
	Table         string `json:"table"`
	Publication   string `json:"publication,omitempty"`
	PDB           string `json:"pdb,omitempty"`
}

type CDCBinding struct {
	TaskID             string `json:"task_id" gorm:"primaryKey"`
	SourceID           string `json:"source_id"`
	ObjectID           string `json:"object_id"`
	Kind               string `json:"kind"`
	ConnectorName      string `json:"connector_name"`
	TopicPrefix        string `json:"topic_prefix"`
	Topic              string `json:"topic"`
	SchemaHistoryTopic string `json:"schema_history_topic,omitempty"`
	Schema             string `json:"schema"`
	Table              string `json:"table"`
	Slot               string `json:"slot,omitempty"`
	Publication        string `json:"publication,omitempty"`
	PDB                string `json:"pdb,omitempty"`
	SourceFingerprint  string `json:"-"`
	SourceVersion      int    `json:"source_version"`
	ConfigVersion      int    `json:"config_version"`
	ServerID           int64  `json:"server_id,omitempty"`
	Legacy             bool   `json:"-" gorm:"-"`
}

var cdcTaskID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var cdcTopicChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func newCDCBinding(taskID string, src SourceSecret, cfg CDCConfigInput, objectID, fingerprint string) (CDCBinding, error) {
	b := CDCBinding{}
	if !cdcTaskID.MatchString(taskID) || !valid(cfg.SourceID, 128) || cfg.SourceVersion < 1 || !valid(objectID, 128) || fingerprint == "" || !valid(cfg.Schema, 128) || !valid(cfg.Table, 128) || strings.ContainsAny(cfg.Schema+cfg.Table+cfg.Publication+cfg.PDB, "\x00\r\n") {
		return b, invalid
	}
	if src.Kind != "postgres" && src.Kind != "mysql" && src.Kind != "oracle" {
		return b, invalid
	}
	if src.Kind == "postgres" && (!valid(cfg.Publication, 63) || cfg.PDB != "") {
		return b, problem(400, "CDC_PUBLICATION_REQUIRED", "请填写由 DBA 为所选表配置的 PostgreSQL 发布名称")
	}
	if src.Kind == "mysql" && (cfg.Schema != src.Database || cfg.Publication != "" || cfg.PDB != "") {
		return b, invalid
	}
	if src.Kind == "oracle" && (!valid(cfg.PDB, 128) || cfg.Publication != "") {
		return b, problem(400, "CDC_PDB_REQUIRED", "请填写 Oracle 目标 PDB 并核对 LogMiner 配置")
	}
	b = CDCBinding{TaskID: taskID, SourceID: cfg.SourceID, ObjectID: objectID, Kind: src.Kind, ConnectorName: "datamesh-cdc-" + taskID, TopicPrefix: "datamesh_cdc_" + taskID, Schema: cfg.Schema, Table: cfg.Table, SourceFingerprint: fingerprint, SourceVersion: cfg.SourceVersion, ConfigVersion: 1}
	b.Topic = b.TopicPrefix + "." + cdcTopicChars.ReplaceAllString(cfg.Schema+"."+cfg.Table, "_")
	if len(b.Topic) > 249 {
		return CDCBinding{}, problem(400, "CDC_TOPIC_TOO_LONG", "库名与表名组合过长，实际采集主题超过249字节，请联系管理员核查名称或路由配置")
	}
	switch src.Kind {
	case "postgres":
		b.Slot = "dm_" + taskID
		b.Publication = cfg.Publication
	case "mysql":
		hash := sha256.Sum256([]byte(taskID))
		b.ServerID = int64(binary.BigEndian.Uint32(hash[:4]))%4000000000 + 10000
		b.SchemaHistoryTopic = b.TopicPrefix + ".schema_history"
	case "oracle":
		b.PDB = cfg.PDB
		b.SchemaHistoryTopic = b.TopicPrefix + ".schema_history"
	}
	return b, nil
}

func resolveCDCBinding(task Record, data TaskData) (CDCBinding, error) {
	if data.Capture != nil {
		b := *data.Capture
		if b.TaskID != task.ID || !cdcTaskID.MatchString(b.TaskID) || b.ConnectorName != "datamesh-cdc-"+b.TaskID {
			return CDCBinding{}, problem(409, "CDC_BINDING_INVALID", "采集作业绑定无效，请管理员核查")
		}
		return b, nil
	}
	switch data.Binding {
	case "postgres", "mysql", "oracle":
	default:
		return CDCBinding{}, invalid
	}
	parts := strings.SplitN(qualified(data.Binding), ".", 2)
	return CDCBinding{TaskID: task.ID, SourceID: data.SourceID, ObjectID: data.ObjectID, Kind: data.Binding, ConnectorName: "datamesh-" + data.Binding, TopicPrefix: "datamesh_" + data.Binding, Topic: topicFor(data.Binding), Schema: parts[0], Table: parts[1], Legacy: true}, nil
}

func cdcIdentityFingerprint(kind, identity, database string) string {
	h := sha256.Sum256(encode([]string{kind, identity, database}))
	return hex.EncodeToString(h[:])
}
