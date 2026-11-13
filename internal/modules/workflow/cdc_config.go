package workflow

import (
	"regexp"
	"strconv"
)

func buildCDCConfig(b CDCBinding, src SourceSecret, credential Credential) (map[string]string, error) {
	if b.Legacy || b.Kind != src.Kind || !cdcTaskID.MatchString(b.TaskID) || b.ConnectorName != "datamesh-cdc-"+b.TaskID || credential.Username == "" || credential.Password == "" {
		return nil, invalid
	}
	cfg := map[string]string{
		"name": b.ConnectorName, "tasks.max": "1", "database.hostname": src.Host, "database.port": strconv.Itoa(src.Port), "database.user": credential.Username, "database.password": credential.Password,
		"topic.prefix": b.TopicPrefix, "table.include.list": "^" + regexp.QuoteMeta(b.Schema+"."+b.Table) + "$", "snapshot.mode": "initial", "topic.creation.default.partitions": "1", "topic.creation.default.replication.factor": "1", "decimal.handling.mode": "string", "tombstones.on.delete": "false",
		"key.converter": "org.apache.kafka.connect.json.JsonConverter", "value.converter": "org.apache.kafka.connect.json.JsonConverter", "key.converter.schemas.enable": "true", "value.converter.schemas.enable": "true",
		"notification.enabled.channels": "sink", "notification.sink.topic.name": b.TopicPrefix + ".notifications", "snapshot.fetch.size": "500", "heartbeat.interval.ms": "10000",
	}
	switch b.Kind {
	case "postgres":
		if b.Slot == "" || b.Publication == "" {
			return nil, invalid
		}
		cfg["connector.class"] = "io.debezium.connector.postgresql.PostgresConnector"
		cfg["database.dbname"] = src.Database
		cfg["plugin.name"] = "pgoutput"
		cfg["slot.name"] = b.Slot
		cfg["publication.name"] = b.Publication
		cfg["publication.autocreate.mode"] = "disabled"
	case "mysql":
		if b.ServerID == 0 || b.SchemaHistoryTopic == "" {
			return nil, invalid
		}
		cfg["connector.class"] = "io.debezium.connector.mysql.MySqlConnector"
		cfg["database.server.id"] = strconv.FormatInt(b.ServerID, 10)
		cfg["database.include.list"] = "^" + regexp.QuoteMeta(src.Database) + "$"
	case "oracle":
		if b.PDB == "" || b.SchemaHistoryTopic == "" {
			return nil, invalid
		}
		cfg["connector.class"] = "io.debezium.connector.oracle.OracleConnector"
		cfg["database.dbname"] = src.Database
		cfg["database.pdb.name"] = b.PDB
		cfg["database.connection.adapter"] = "logminer"
		cfg["log.mining.strategy"] = "online_catalog"
		cfg["log.mining.flush.table.name"] = "DMF_" + b.TaskID[:24]
	default:
		return nil, invalid
	}
	if b.SchemaHistoryTopic != "" {
		cfg["schema.history.internal.kafka.bootstrap.servers"] = "kafka:9092"
		cfg["schema.history.internal.kafka.topic"] = b.SchemaHistoryTopic
		cfg["include.schema.changes"] = "false"
	}
	return cfg, nil
}
