package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernedBindingInput struct {
	SourceID        string       `json:"source_id"`
	SourceVersion   int          `json:"source_version"`
	Schema          string       `json:"schema"`
	Table           string       `json:"table"`
	InputArchiveID  string       `json:"input_archive_id"`
	ContractVersion int          `json:"contract_version"`
	RuleVersion     int          `json:"rule_version"`
	Mapping         []ETLMapping `json:"mapping"`
}
type GovernedResultBinding struct {
	GovernedBindingInput
	DatasetID        string `json:"dataset_id"`
	InputSnapshotID  string `json:"input_snapshot_id"`
	ResultSnapshotID string `json:"result_snapshot_id"`
}
type GovernedCheck struct {
	ID               string          `gorm:"primaryKey" json:"check_id"`
	DatasetID        string          `json:"dataset_id"`
	ActorID          string          `json:"-"`
	Definition       json.RawMessage `gorm:"type:jsonb" json:"-"`
	State            string          `json:"state"`
	ResultSnapshotID string          `json:"result_snapshot_id"`
	Processed        int64           `json:"processed_rows"`
	ErrorCode        string          `json:"error_code,omitempty"`
	LeaseOwner       string          `json:"-"`
	LeaseExpiresAt   time.Time       `json:"-"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (GovernedCheck) TableName() string { return "governed_checks" }

func (s *Server) registerGovernedBinding(api, write *gin.RouterGroup) {
	write.POST("/datasets/:id/result-binding", s.require("governance", "governor"), s.mutatePath("bind_governed_result", s.bindGovernedResult))
	api.GET("/datasets/:id/result-binding", s.require("governance", "governor"), func(c *gin.Context) {
		record, e := get(s.db.WithContext(c.Request.Context()), "dataset", c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if e = requireResourceCapability(s.db.WithContext(c.Request.Context()), principal(c), Resource{Type: "dataset", ID: record.ID}, "govern_resource"); e != nil {
			s.fail(c, e)
			return
		}
		data, e := decode[DatasetData](record.Body)
		if e != nil {
			s.fail(c, e)
			return
		}
		var jobs []GovernedCheck
		if e = s.db.WithContext(c.Request.Context()).Where("dataset_id=?", record.ID).Order("created_at DESC").Limit(10).Find(&jobs).Error; e != nil {
			s.fail(c, e)
			return
		}
		var tasks []Record
		if e = s.db.WithContext(c.Request.Context()).Where("kind='task' AND body->>'object_id'=?", data.Definition.SourceObjectID).Limit(1).Find(&tasks).Error; e != nil {
			s.fail(c, e)
			return
		}
		archiveID := ""
		if len(tasks) > 0 {
			capture, err := decode[TaskData](tasks[0].Body)
			if err != nil {
				s.fail(c, err)
				return
			}
			archiveID = capture.ArchiveID
		}
		c.JSON(200, gin.H{"binding": data.ResultBinding, "checks": jobs, "rule_version": data.RuleVersion, "contract_version": record.Version, "input_archive_id": archiveID})
	})
}
func (s *Server) governedBindingBasis(ctx context.Context, tx *gorm.DB, p identity.Principal, dataset Record, input GovernedBindingInput) (DatasetData, Record, Object, error) {
	data, e := decode[DatasetData](dataset.Body)
	if e == nil && data.GovernanceRunID != "" {
		return data, Record{}, Object{}, problem(409, "GOVERNANCE_OUTPUT_IMMUTABLE", "数据流成果由执行自动登记，不能重新绑定手工结果")
	}
	if e != nil {
		return data, Record{}, Object{}, e
	}
	if !platform(p, "governance") || !role(p, "governor") {
		return data, Record{}, Object{}, denied
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "dataset", ID: dataset.ID}, "govern_resource"); e != nil {
		return data, Record{}, Object{}, e
	}
	if e = requireDatasetProcessingBasis(tx, dataset, p); e != nil {
		return data, Record{}, Object{}, e
	}
	if input.ContractVersion != dataset.Version || input.RuleVersion != data.RuleVersion {
		return data, Record{}, Object{}, problem(409, "RESULT_VERSION_CHANGED", "治理成果契约或规则版本已变化")
	}
	objectRecord, e := get(tx, "object", data.Definition.SourceObjectID, false)
	if e != nil {
		return data, Record{}, Object{}, e
	}
	object, e := decode[Object](objectRecord.Body)
	if e != nil {
		return data, Record{}, Object{}, e
	}
	rule, e := ruleVersion(tx, data.RuleID, data.RuleVersion)
	if e != nil {
		return data, Record{}, Object{}, e
	}
	definition, e := decode[RuleData](rule.Body)
	if e != nil {
		return data, Record{}, Object{}, e
	}
	if definition.Definition.ObjectID != objectRecord.ID || definition.Definition.SchemaVersion != objectRecord.Version || object.SchemaVersion != objectRecord.Version {
		return data, Record{}, Object{}, problem(409, "RESULT_VERSION_CHANGED", "治理规则与原始对象版本不一致")
	}
	var task Record
	if e = tx.Where("kind='task' AND body->>'object_id'=?", objectRecord.ID).Take(&task).Error; e != nil {
		return data, task, object, problem(409, "ARCHIVE_REQUIRED", "请先归档来源采集范围")
	}
	capture, e := decode[TaskData](task.Body)
	if e != nil {
		return data, task, object, e
	}
	if capture.ArchiveID != input.InputArchiveID || capture.SchemaVersion != objectRecord.Version {
		return data, task, object, problem(409, "RESULT_INPUT_CHANGED", "结果绑定的原始归档范围已变化，请重新选择")
	}
	if !valid(input.Schema, 128) || !valid(input.Table, 128) || len(input.Mapping) != len(data.Definition.Fields) {
		return data, task, object, invalid
	}
	selected := TableConfig{Fields: data.Definition.Fields, Keys: []string{}}
	for _, field := range object.Fields {
		if field.PrimaryKey {
			if !slices.ContainsFunc(selected.Fields, func(f Field) bool { return f.Name == field.Name }) {
				return data, task, object, problem(409, "RESULT_KEY_REQUIRED", "治理成果必须保留全部原始主键")
			}
			selected.Keys = append(selected.Keys, field.Name)
		}
	}
	for _, m := range input.Mapping {
		if m.Source != m.Target || (slices.Contains(selected.Keys, m.Source) && m.Rule != "copy") {
			return data, task, object, problem(400, "RESULT_MAPPING_INVALID", "结果映射须保留目录字段名称和原始主键，主键只能直接映射")
		}
	}
	if e = validateETLMapping(input.Mapping, selected, selected); e != nil {
		return data, task, object, e
	}
	if e = requireProcessingAuthorization(tx, p, Resource{Type: "raw_object", ID: objectRecord.ID, Version: objectRecord.Version}, fieldNames(selected.Fields), "数据治理"); e != nil {
		return data, task, object, e
	}
	return data, task, object, nil
}
func fieldNames(fields []Field) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}
func (s *Server) bindGovernedResult(tx *gorm.DB, p identity.Principal, id string, body []byte) (int, json.RawMessage, string, error) {
	input, e := bind[GovernedBindingInput](body)
	if e != nil {
		return 0, nil, id, e
	}
	if e := governanceDatasetWorkspaceWrite(tx, p, id); e != nil {
		return 0, nil, id, e
	}
	dataset, e := get(tx, "dataset", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if dataset.State == "published" {
		return 0, nil, id, problem(409, "RESULT_WITHDRAW_REQUIRED", "请先下架治理成果，再修改结果绑定")
	}
	data, _, _, e := s.governedBindingBasis(tx.Statement.Context, tx, p, dataset, input)
	if e != nil {
		return 0, nil, id, e
	}
	source, src, db, e := s.sourceForRead(tx.Statement.Context, tx, p, input.SourceID)
	if e != nil {
		return 0, nil, id, e
	}
	defer db.Close()
	if source.Version != input.SourceVersion {
		return 0, nil, id, problem(409, "SOURCE_VERSION_CHANGED", "结果数据源版本已变化")
	}
	if src.Kind != "postgres" && src.Kind != "mysql" {
		return 0, nil, id, problem(409, "RESULT_ADAPTER_UNAVAILABLE", "动态治理结果目前支持 PostgreSQL/MySQL 库表")
	}
	physical, e := discoverTable(tx.Statement.Context, db, src, input.Schema, input.Table)
	if e != nil {
		return 0, nil, id, e
	}
	expected := TableConfig{Fields: data.Definition.Fields, Keys: []string{}}
	for _, field := range expected.Fields {
		if field.PrimaryKey {
			expected.Keys = append(expected.Keys, field.Name)
		}
	}
	if !slices.Equal(expected.Keys, physical.Keys) {
		return 0, nil, id, problem(409, "RESULT_KEY_REQUIRED", "结果表主键须与治理成果主键完全一致")
	}
	if e = validateETLMapping(input.Mapping, expected, physical); e != nil {
		return 0, nil, id, e
	}
	var pending int64
	if e = tx.Model(&GovernedCheck{}).Where("dataset_id=? AND state IN ('queued','checking')", id).Count(&pending).Error; e != nil {
		return 0, nil, id, e
	}
	if pending > 0 {
		return 0, nil, id, problem(409, "RESULT_CHECK_PENDING", "已有结果核验正在进行，请等待完成")
	}
	jobID, e := newID()
	if e != nil {
		return 0, nil, id, e
	}
	snapshotID, e := newID()
	if e != nil {
		return 0, nil, id, e
	}
	job := GovernedCheck{ID: jobID, DatasetID: id, ActorID: p.UserID, Definition: encode(input), State: "queued", ResultSnapshotID: snapshotID}
	if e = tx.Create(&job).Error; e != nil {
		return 0, nil, id, e
	}
	data.ResultCheckID = job.ID
	data.ResultBinding = nil
	data.QualityStatus = "checking"
	data.CompletenessStatus = "pending"
	data.Quality = json.RawMessage("null")
	dataset.Body = encode(data)
	if e = save(tx, &dataset); e != nil {
		return 0, nil, id, e
	}
	return 202, encode(gin.H{"dataset_id": id, "governed_check_id": job.ID, "state": "queued"}), id, nil
}

func (s *Server) governedReady(ctx context.Context, tx *gorm.DB, dataset Record) (DatasetData, Quality, string, error) {
	data, e := decode[DatasetData](dataset.Body)
	if e != nil {
		return data, Quality{}, "", e
	}
	binding := data.ResultBinding
	if binding == nil {
		return data, Quality{}, "", problem(409, "RESULT_BINDING_REQUIRED", "请配置并核验实际治理结果库表")
	}
	if binding.DatasetID != dataset.ID || binding.ContractVersion != dataset.Version || binding.RuleVersion != data.RuleVersion {
		return data, Quality{}, "", problem(409, "RESULT_VERSION_CHANGED", "结果绑定与契约、规则版本不一致")
	}
	var task Record
	if e = tx.Where("kind='task' AND body->>'object_id'=?", data.Definition.SourceObjectID).Take(&task).Error; e != nil {
		return data, Quality{}, "", e
	}
	capture, e := decode[TaskData](task.Body)
	if e != nil {
		return data, Quality{}, "", e
	}
	if capture.ArchiveID != binding.InputArchiveID {
		return data, Quality{}, "", problem(409, "RESULT_INPUT_CHANGED", "来源归档已变化，请重新核验治理结果")
	}
	object, e := get(tx, "object", data.Definition.SourceObjectID, false)
	if e != nil {
		return data, Quality{}, "", e
	}
	rule, e := ruleVersion(tx, data.RuleID, data.RuleVersion)
	if e != nil {
		return data, Quality{}, "", e
	}
	def, e := decode[RuleData](rule.Body)
	if e != nil {
		return data, Quality{}, "", e
	}
	if def.Definition.SchemaVersion != object.Version || capture.SchemaVersion != object.Version {
		return data, Quality{}, "", problem(409, "RESULT_VERSION_CHANGED", "原始对象或规则版本已变化")
	}
	var snapshot ResourceSnapshot
	if e = tx.Where("id=? AND state='ready' AND scope=? AND object_version=?", binding.ResultSnapshotID, binding.ResultSnapshotID, object.Version).Take(&snapshot).Error; e != nil {
		return data, Quality{}, "", problem(409, "RESULT_SNAPSHOT_UNAVAILABLE", "治理结果快照不可用，请重新核验")
	}
	var quality Quality
	if json.Unmarshal(data.Quality, &quality) != nil || quality.Status != "passed" || quality.ContractVersion != dataset.Version || quality.RuleVersion != data.RuleVersion {
		return data, quality, "", problem(409, "QUALITY_BLOCKED", "完整范围质量核验尚未通过")
	}
	return data, quality, snapshot.ID, nil
}

// The snapshot and check leases are fenced together before any encrypted result rows are stored.
func (s *Server) governedCheckFence(tx *gorm.DB, id, token string) error {
	var job GovernedCheck
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND lease_owner=? AND lease_expires_at>CURRENT_TIMESTAMP AND state='checking'", id, token).Take(&job).Error; e != nil {
		return problem(409, "RESULT_LEASE_LOST", "治理核验租约已失效")
	}
	return nil
}
