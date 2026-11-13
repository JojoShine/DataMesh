package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Physical identity prevents another registration of the same database from bypassing capture ownership.
func cdcDatabaseIdentity(ctx context.Context, db *sql.DB, src SourceSecret, cfg CDCConfigInput) (string, error) {
	var id string
	q := "SELECT system_identifier::text FROM pg_control_system()"
	if src.Kind == "mysql" {
		q = "SELECT @@server_uuid"
	}
	if src.Kind == "oracle" {
		q = "SELECT TO_CHAR(dbid) FROM v$database"
	}
	if e := db.QueryRowContext(ctx, q).Scan(&id); e != nil || id == "" {
		return "", problem(409, "CDC_IDENTITY_UNVERIFIED", "无法核对数据库实例标识，请 DBA 为采集账号开放 PostgreSQL pg_control_system()、MySQL server_uuid 或 Oracle v$database 的只读查询后重试")
	}
	database := src.Database
	if src.Kind == "oracle" {
		database = cfg.PDB
	}
	return cdcIdentityFingerprint(src.Kind, id, database), nil
}

func (s *Server) createCDCTask(tx *gorm.DB, p identity.Principal, raw []byte) (int, json.RawMessage, string, error) {
	v, e := bind[CDCConfigInput](raw)
	if e != nil {
		return 0, nil, "", e
	}
	source, src, db, e := s.cdcSourceForRead(tx.Statement.Context, tx, p, v.SourceID, v.PDB)
	if e != nil {
		return 0, nil, "", e
	}
	defer db.Close()
	if source.Version != v.SourceVersion {
		return 0, nil, "", problem(409, "SOURCE_VERSION_CHANGED", "数据源配置已变化，请刷新后重新选择")
	}
	table, e := discoverTable(tx.Statement.Context, db, src, v.Schema, v.Table)
	if e != nil {
		return 0, nil, "", e
	}
	tables, e := discoverTables(tx.Statement.Context, db, src)
	if e != nil {
		return 0, nil, "", e
	}
	for _, t := range tables {
		if t.Schema == v.Schema && t.Name == v.Table && strings.Contains(t.Kind, "VIEW") {
			return 0, nil, "", problem(400, "CDC_TABLE_REQUIRED", "CDC 采集对象必须为有主键的实际库表，不能选择视图")
		}
	}
	if len(table.Keys) == 0 {
		return 0, nil, "", problem(400, "CDC_PRIMARY_KEY_REQUIRED", "所选表缺少主键，请 DBA 配置主键后刷新库表")
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	fingerprint, e := cdcDatabaseIdentity(tx.Statement.Context, db, src, v)
	if e != nil {
		return 0, nil, "", e
	}
	objectID := v.SourceID + "-" + cdcIdentityFingerprint("object", v.Schema, v.Table)[:24]
	binding, e := newCDCBinding(id, src, v, objectID, fingerprint)
	if e != nil {
		return 0, nil, "", e
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", fingerprint).Error; e != nil {
		return 0, nil, "", e
	}
	var count int64
	if e = tx.Model(&CDCBinding{}).Where("source_fingerprint=? AND schema=? AND \"table\"=?", fingerprint, v.Schema, v.Table).Count(&count).Error; e != nil {
		return 0, nil, "", e
	}
	if count > 0 {
		return 0, nil, "", problem(409, "CDC_TABLE_ALREADY_BOUND", "该数据库表已建立采集任务，请维护已有任务，避免重置位点或重复采集")
	}
	// A legacy task owns its source's fixed table. Do not create a competing slot via a new registration.
	var legacy []Record
	if e = tx.Where("kind='task' AND body->'capture_binding' IS NULL").Find(&legacy).Error; e != nil {
		return 0, nil, "", e
	}
	for _, old := range legacy {
		data, err := decode[TaskData](old.Body)
		if err != nil {
			return 0, nil, "", err
		}
		if data.Binding == src.Kind && qualified(src.Kind) == v.Schema+"."+v.Table {
			oldSource, err := get(tx, "source", old.ParentID, false)
			if err != nil {
				return 0, nil, "", err
			}
			oldConfig, err := decode[SourceSecret](oldSource.Body)
			if err != nil {
				return 0, nil, "", err
			}
			legacyDB, err := s.runtime.database(tx.Statement.Context, tx, oldConfig)
			if err != nil {
				return 0, nil, "", problem(409, "CDC_LEGACY_IDENTITY_UNVERIFIED", "无法核查旧采集源实例身份，请恢复旧数据源连接后重试")
			}
			legacyIdentity, err := cdcDatabaseIdentity(tx.Statement.Context, legacyDB, oldConfig, CDCConfigInput{PDB: oldConfig.Database})
			legacyDB.Close()
			if err != nil {
				return 0, nil, "", err
			}
			if legacyIdentity == fingerprint {
				return 0, nil, "", problem(409, "CDC_LEGACY_SCOPE_RESERVED", "该库表已有样例采集绑定，请先核查原任务归属，不能建立竞争采集作业")
			}
		}
	}
	var existing Record
	lookup := tx.Where("kind='object' AND id=?", objectID).Find(&existing)
	if lookup.Error != nil {
		return 0, nil, "", lookup.Error
	}
	objectVersion := 1
	if lookup.RowsAffected > 0 {
		old, err := decode[Object](existing.Body)
		if err != nil {
			return 0, nil, "", err
		}
		if string(encode(old.Fields)) != string(encode(table.Fields)) {
			return 0, nil, "", problem(409, "CDC_SCHEMA_CHANGED", "对象结构已变化，请核查现有目录和授权后登记新结构版本")
		}
		objectVersion = existing.Version
	} else {
		obj := Object{ObjectID: objectID, SourceID: source.ID, QualifiedName: v.Schema + "." + v.Table, SchemaVersion: objectVersion, Fields: table.Fields}
		existing = Record{Kind: "object", ID: objectID, DepartmentID: source.DepartmentID, OwnerID: source.OwnerID, ParentID: source.ID, State: "available", Version: objectVersion, Body: encode(obj)}
		if e = tx.Create(&existing).Error; e != nil {
			return 0, nil, "", e
		}
	}
	data := TaskData{TaskInput: TaskInput{SourceID: source.ID, ObjectID: objectID, Mode: "cdc", SchemaVersion: objectVersion}, Binding: src.Kind, Capture: &binding}
	task := Record{Kind: "task", ID: id, DepartmentID: source.DepartmentID, OwnerID: p.UserID, ParentID: source.ID, State: "draft", Version: 1, Body: encode(data)}
	if e = tx.Create(&task).Error; e != nil {
		return 0, nil, "", e
	}
	if e = tx.Create(&binding).Error; e != nil {
		return 0, nil, "", e
	}
	if e = tx.Exec("INSERT INTO cdc_controls(task_id) VALUES (?)", id).Error; e != nil {
		return 0, nil, "", e
	}
	if e = tx.Exec("INSERT INTO cdc_snapshot_progress(task_id) VALUES (?)", id).Error; e != nil {
		return 0, nil, "", e
	}
	return 201, encode(gin.H{"task_id": id, "run_id": nil, "source_id": source.ID, "object_id": objectID, "state": "draft", "config_version": 1, "capture_binding": binding, "ingestion": progress("pending"), "processing": progress("pending"), "result": progress("pending"), "archive": progress("pending")}), id, nil
}
