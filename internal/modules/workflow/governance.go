package workflow

import (
	"context"
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) registerGovernance(api, write *gin.RouterGroup) {
	s.registerGovernedBinding(api, write)
	s.registerGovernanceOutputs(api)
	s.registerGovernanceCatalog(api, write)
	write.POST("/rules", s.require("governance", "governor"), s.mutate("create_rule", s.createRule))
	write.POST("/rules/:id/versions", s.require("governance", "governor"), s.mutatePath("new_rule_version", s.newRuleVersion))
	write.POST("/rules/:id/publish", s.require("governance", "governor"), s.mutatePath("publish_rule", s.publishRule))
	write.POST("/datasets", s.require("assets", "provider"), s.mutate("create_dataset", s.createDataset))
	write.POST("/tasks/:id/archive", s.require("exchange", "operator"), s.archiveAction)
	write.POST("/datasets/:id/check", s.require("governance", "governor"), s.mutatePath("check_dataset", s.checkDataset))
	write.POST("/datasets/:id/publish", s.require("assets", "provider"), s.mutatePath("publish_dataset", s.publishDataset))
	write.POST("/datasets/:id/withdraw", s.require("assets", "provider"), s.mutatePath("withdraw_dataset", s.withdrawDataset))
	api.GET("/datasets/:id/quality", s.require("governance", "governor"), s.quality)
	api.GET("/datasets/:id/lineage", s.require("governance", "governor"), s.lineage)
}
func validateRule(v RuleInput, obj Object) error {
	if !valid(v.Name, 200) || v.SchemaVersion != obj.SchemaVersion || len(v.Checks) > 100 || len(v.Checks) == 0 {
		return invalid
	}
	fields := map[string]Field{}
	for _, f := range obj.Fields {
		fields[f.Name] = f
	}
	for _, check := range v.Checks {
		f, ok := fields[check.Field]
		if !ok {
			return invalid
		}
		switch check.Kind {
		case "format":
			if f.LogicalType != "string" || len(check.Parameters) != 1 || qualityFormatPattern(check) == "" {
				return invalid
			}
		case "required":
			if len(check.Parameters) != 0 {
				return invalid
			}
		case "business_key":
			if !f.PrimaryKey || len(check.Parameters) != 0 {
				return invalid
			}
		case "type":
			var value string
			if json.Unmarshal(check.Parameters["type"], &value) != nil || value != f.LogicalType || len(check.Parameters) != 1 {
				return invalid
			}
		case "length", "range":
			if len(check.Parameters) == 0 || len(check.Parameters) > 2 {
				return invalid
			}
			for key, value := range check.Parameters {
				if key != "min" && key != "max" {
					return invalid
				}
				n, ok := new(big.Rat).SetString(string(value))
				if !ok {
					return invalid
				}
				if check.Kind == "length" && (!n.IsInt() || n.Sign() < 0) {
					return invalid
				}
			}
			if a, ok := check.Parameters["min"]; ok {
				if b, ok := check.Parameters["max"]; ok {
					left, _ := new(big.Rat).SetString(string(a))
					right, _ := new(big.Rat).SetString(string(b))
					if left.Cmp(right) > 0 {
						return invalid
					}
				}
			}
		case "enum":
			var values []json.RawMessage
			if len(check.Parameters) != 1 || json.Unmarshal(check.Parameters["values"], &values) != nil || len(values) == 0 || len(values) > 100 {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}
func (s *Server) createRule(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[RuleInput](b)
	if e != nil {
		return 0, nil, "", e
	}
	obj, e := get(tx, "object", v.ObjectID, false)
	if e != nil {
		return 0, nil, "", e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: "object", ID: obj.ID}, "govern_resource"); err != nil {
		return 0, nil, "", denied
	}
	object, e := decode[Object](obj.Body)
	if e != nil {
		return 0, nil, "", e
	}
	if e = validateRule(v, object); e != nil {
		return 0, nil, "", e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	r := Record{Kind: "rule", ID: id, DepartmentID: obj.DepartmentID, OwnerID: p.UserID, ParentID: obj.ID, State: "draft", Version: 1, Body: encode(RuleData{Definition: v})}
	if e = tx.Create(&r).Error; e != nil {
		return 0, nil, "", e
	}
	result, e := s.view(tx.Statement.Context, r)
	return 201, result, r.ID, e
}
func (s *Server) newRuleVersion(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	r, e := get(tx, "rule", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "govern_resource"); err != nil {
		return 0, nil, id, err
	}
	if r.State != "published" {
		return 0, nil, id, conflict
	}
	v, e := bind[RuleInput](b)
	if e != nil {
		return 0, nil, id, e
	}
	obj, e := get(tx, "object", r.ParentID, false)
	if e != nil {
		return 0, nil, id, e
	}
	object, e := decode[Object](obj.Body)
	if e != nil {
		return 0, nil, id, e
	}
	if v.ObjectID != object.ObjectID {
		return 0, nil, id, invalid
	}
	if e = validateRule(v, object); e != nil {
		return 0, nil, id, e
	}
	snapshot := r
	snapshot.Kind = "rule_version"
	snapshot.ID = id + "-v" + strconv.Itoa(r.Version)
	if e = tx.Create(&snapshot).Error; e != nil {
		return 0, nil, id, e
	}
	r.Version++
	r.State = "draft"
	r.Body = encode(RuleData{Definition: v, PreviousID: snapshot.ID})
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 201, out, id, e
}
func (s *Server) publishRule(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	r, e := get(tx, "rule", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "govern_resource"); err != nil {
		return 0, nil, id, err
	}
	if r.State != "draft" {
		return 0, nil, id, conflict
	}
	r.State = "published"
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 200, out, id, e
}
func ruleVersion(tx *gorm.DB, id string, version int) (Record, error) {
	r, e := get(tx, "rule", id, false)
	if e != nil {
		return r, e
	}
	if r.Version == version && r.State == "published" {
		return r, nil
	}
	return get(tx, "rule_version", id+"-v"+strconv.Itoa(version), false)
}
func (s *Server) createDataset(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[DatasetInput](b)
	if e != nil {
		return 0, nil, "", e
	}
	if !valid(v.Name, 200) || !valid(v.Description, 2000) || !valid(v.UsageRestrictions, 2000) || v.OwnerID != p.UserID || len(v.Fields) == 0 || len(v.Fields) > 100 {
		return 0, nil, "", invalid
	}
	obj, e := get(tx, "object", v.SourceObjectID, false)
	if e != nil {
		return 0, nil, "", e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: "object", ID: obj.ID}, "manage_resource"); err != nil {
		return 0, nil, "", denied
	}
	object, e := decode[Object](obj.Body)
	if e != nil {
		return 0, nil, "", e
	}
	allowed := map[string]Field{}
	for _, f := range object.Fields {
		allowed[f.Name] = f
	}
	seen := map[string]bool{}
	for _, f := range v.Fields {
		known, ok := allowed[f.Name]
		if !ok || seen[f.Name] || f.LogicalType != known.LogicalType || f.Nullable != known.Nullable || f.PrimaryKey != known.PrimaryKey || (known.Sensitive && !f.Sensitive) {
			return 0, nil, "", invalid
		}
		seen[f.Name] = true
	}
	if !seen["id"] {
		return 0, nil, "", invalid
	}
	var rule Record
	if e = tx.Where("kind='rule' AND parent_id=? AND state='published'", obj.ID).Order("updated_at DESC").Take(&rule).Error; e != nil {
		return 0, nil, "", problem(409, "RULE_REQUIRED", "请先为此对象发布质量规则")
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	names := []string{}
	for _, f := range v.Fields {
		names = append(names, f.Name)
	}
	if e = requireProcessingAuthorization(tx, p, Resource{Type: "raw_object", ID: obj.ID, Version: obj.Version}, names, "数据治理"); e != nil {
		return 0, nil, "", e
	}
	data := DatasetData{Definition: v, RuleID: rule.ID, RuleVersion: rule.Version, QualityStatus: "not_checked", CompletenessStatus: "pending", LatestWatermark: json.RawMessage("null"), Quality: json.RawMessage("null")}
	r := Record{Kind: "dataset", ID: id, DepartmentID: obj.DepartmentID, OwnerID: p.UserID, ParentID: obj.ID, State: "draft", Version: 1, Body: encode(data)}
	if e = tx.Create(&r).Error; e != nil {
		return 0, nil, "", e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 201, out, id, e
}

type Failure struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}
type Quality struct {
	DatasetID       string          `json:"dataset_id"`
	ContractVersion int             `json:"contract_version"`
	RuleVersion     int             `json:"rule_version"`
	Status          string          `json:"status"`
	Passed          int             `json:"passed_count"`
	Failed          int             `json:"failed_count"`
	CheckedRange    json.RawMessage `json:"checked_range"`
	FailureReasons  []Failure       `json:"failure_reasons"`
}

func evaluate(def RuleInput, rows []DataRow) (int, int, []Failure) {
	passed, failed := 0, 0
	reasons := map[string]int{}
	seen := map[string]map[string]bool{}
	for _, row := range rows {
		bad := false
		for _, check := range def.Checks {
			value, exists := row[check.Field]
			isNull := !exists || string(value) == "null"
			ok := true
			switch check.Kind {
			case "format":
				if !isNull {
					var text string
					ok = json.Unmarshal(value, &text) == nil && qualityFormatPattern(check) != "" && regexp.MustCompile(qualityFormatPattern(check)).MatchString(text)
				}
			case "required":
				ok = !isNull
			case "business_key":
				if isNull {
					ok = false
				} else {
					if seen[check.Field] == nil {
						seen[check.Field] = map[string]bool{}
					}
					ok = !seen[check.Field][string(value)]
					seen[check.Field][string(value)] = true
				}
			case "type":
				if !isNull {
					var target string
					_ = json.Unmarshal(check.Parameters["type"], &target)
					if target == "string" {
						var text string
						ok = json.Unmarshal(value, &text) == nil
					} else if target == "integer" {
						var n int64
						ok = json.Unmarshal(value, &n) == nil
					} else if target == "decimal" {
						var text string
						ok = json.Unmarshal(value, &text) == nil
						if ok {
							_, ok = new(big.Rat).SetString(text)
						}
					} else {
						ok = false
					}
				}
			case "length", "range":
				if !isNull {
					var n *big.Rat
					var text string
					if json.Unmarshal(value, &text) != nil {
						text = string(value)
					}
					if check.Kind == "length" {
						n = big.NewRat(int64(utf8.RuneCountInString(text)), 1)
					} else {
						n, _ = new(big.Rat).SetString(text)
					}
					ok = n != nil
					if ok {
						for bound, raw := range check.Parameters {
							limit, _ := new(big.Rat).SetString(string(raw))
							if (bound == "min" && n.Cmp(limit) < 0) || (bound == "max" && n.Cmp(limit) > 0) {
								ok = false
							}
						}
					}
				}
			case "enum":
				if !isNull {
					var values []json.RawMessage
					_ = json.Unmarshal(check.Parameters["values"], &values)
					ok = false
					for _, entry := range values {
						if string(entry) == string(value) {
							ok = true
						}
					}
				}
			}
			if !ok {
				reasons[check.Kind+":"+check.Field]++
				if check.Blocking {
					bad = true
				}
			}
		}
		if bad {
			failed++
		} else {
			passed++
		}
	}
	out := []Failure{}
	for code, count := range reasons {
		out = append(out, Failure{code, count})
	}
	slices.SortFunc(out, func(a, b Failure) int {
		if a.Code < b.Code {
			return -1
		}
		if a.Code > b.Code {
			return 1
		}
		return 0
	})
	return passed, failed, out
}
func (s *Server) assess(ctx context.Context, tx *gorm.DB, dataset Record) (DatasetData, Quality, []DataRow, error) {
	data, e := decode[DatasetData](dataset.Body)
	if e != nil {
		return data, Quality{}, nil, e
	}
	if data.GovernanceRunID != "" {
		ready, quality, _, report, err := s.governanceOutputReady(ctx, tx, dataset)
		if err != nil {
			return ready, quality, nil, err
		}
		if report.OutputRows > 10000 {
			return ready, quality, nil, problem(409, "GOVERNANCE_USE_PAGED_OUTPUT", "请通过分页或批量成果适配器读取")
		}
		rows := []DataRow{}
		for start := 0; start < int(report.OutputRows); start += 1000 {
			page, err := s.governanceOutputPage(ctx, tx, dataset, start, min(1000, int(report.OutputRows)-start))
			if err != nil {
				return ready, quality, nil, err
			}
			rows = append(rows, page...)
		}
		return ready, quality, rows, nil
	}
	if data.ResultCheckID != "" && data.ResultBinding == nil {
		return data, Quality{DatasetID: dataset.ID, ContractVersion: dataset.Version, RuleVersion: data.RuleVersion, Status: "blocked"}, nil, problem(409, "QUALITY_BLOCKED", "显式结果绑定尚未完成完整范围核验，请查看结果检查记录")
	}
	if data.ResultBinding != nil {
		bound, q, id, e := s.governedReady(ctx, tx, dataset)
		if e != nil {
			return bound, q, nil, e
		}
		var count int64
		if e = s.db.WithContext(ctx).Model(&ResourceSnapshot{}).Where("id=?", id).Select("row_count").Scan(&count).Error; e != nil {
			return bound, q, nil, e
		}
		if count > 10000 {
			return bound, q, nil, problem(409, "RESULT_BULK_REQUIRED", "此结果超过普通整份查询上限，请使用分页交付或 ETL 批量交换")
		}
		rows := []DataRow{}
		e = s.rawSnapshotRows(ctx, id, func(row DataRow) error { rows = append(rows, row); return nil })
		return bound, q, rows, e
	}
	quality := Quality{DatasetID: dataset.ID, ContractVersion: dataset.Version, RuleVersion: data.RuleVersion, Status: "blocked", CheckedRange: encode(gin.H{}), FailureReasons: []Failure{}}
	source, src, e := s.sourceForObject(ctx, data.Definition.SourceObjectID)
	if e != nil {
		return data, quality, nil, e
	}
	obj, err := get(tx, "object", data.Definition.SourceObjectID, false)
	if err != nil {
		return data, quality, nil, err
	}
	object, err := decode[Object](obj.Body)
	if err != nil {
		return data, quality, nil, err
	}
	if src.Kind != "postgres" || object.QualifiedName != "public.customers" {
		return data, quality, nil, problem(409, "RESULT_TEMPLATE_UNAVAILABLE", "此来源尚未配置治理结果模板，不允许发布")
	}
	var task Record
	if e = tx.Where("kind='task' AND parent_id=?", source.ID).Take(&task).Error; e != nil {
		return data, quality, nil, problem(409, "TASK_REQUIRED", "请先建立接入任务")
	}
	taskData, e := decode[TaskData](task.Body)
	if e != nil {
		return data, quality, nil, e
	}
	if taskData.Capture != nil {
		return data, quality, nil, problem(409, "RESULT_BINDING_REQUIRED", "该采集任务尚未绑定可验证的治理结果，不能使用样例结果替代")
	}
	if e = s.runtime.compute(ctx); e != nil {
		return data, quality, nil, e
	}
	first, end, e := s.runtime.end(ctx, topicFor(src.Kind))
	if e != nil {
		return data, quality, nil, e
	}
	if taskData.ArchiveID == "" {
		data.CompletenessStatus = "archive_gap"
		return data, quality, nil, problem(409, "ARCHIVE_REQUIRED", "请先归档当前原始范围")
	}
	archive, e := get(tx, "archive", taskData.ArchiveID, false)
	if e != nil {
		return data, quality, nil, e
	}
	meta, e := decode[ArchiveData](archive.Body)
	if e != nil {
		return data, quality, nil, e
	}
	if first != 0 || meta.Start != 0 || meta.End != end {
		data.CompletenessStatus = "archive_gap"
		return data, quality, nil, problem(409, "ARCHIVE_GAP", "原始数据已变化，请归档新范围后重新核对")
	}
	if _, e = s.rawRecords(ctx, meta); e != nil {
		return data, quality, nil, problem(503, "ARCHIVE_UNAVAILABLE", "原始归档读取或校验失败，暂不开放治理交付")
	}
	result, e := s.runtime.result(ctx)
	if e != nil {
		data.CompletenessStatus = "result_gap"
		return data, quality, nil, e
	}
	sourceRows, e := s.runtime.sourceRows(ctx, tx, src)
	if e != nil {
		return data, quality, nil, e
	}
	if !sameRows(sourceRows, result) {
		data.CompletenessStatus = "result_gap"
		return data, quality, nil, problem(409, "RECONCILIATION_FAILED", "来源与治理结果尚未对齐，暂不允许交付")
	}
	_, after, e := s.runtime.end(ctx, topicFor(src.Kind))
	if e != nil {
		return data, quality, nil, e
	}
	if after != end {
		return data, quality, nil, problem(409, "WATERMARK_CHANGED", "核对期间接入水位发生变化，请稍后重试")
	}
	rule, e := ruleVersion(tx, data.RuleID, data.RuleVersion)
	if e != nil {
		return data, quality, nil, e
	}
	definition, e := decode[RuleData](rule.Body)
	if e != nil {
		return data, quality, nil, e
	}
	quality.Passed, quality.Failed, quality.FailureReasons = evaluate(definition.Definition, result)
	if quality.Failed == 0 {
		quality.Status = "passed"
	}
	quality.CheckedRange = encode(gin.H{"topic": meta.Topic, "partition": 0, "end_offset_exclusive": end, "manifest_id": archive.ID, "checked_at": time.Now().UTC(), "row_count": len(result)})
	data.QualityStatus = quality.Status
	data.CompletenessStatus = "complete"
	data.LatestWatermark = quality.CheckedRange
	data.Quality = encode(quality)
	return data, quality, result, nil
}
func (s *Server) checkDataset(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if e := governanceDatasetWorkspaceWrite(tx, p, id); e != nil {
		return 0, nil, id, e
	}
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	r, e := get(tx, "dataset", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "govern_resource"); err != nil {
		return 0, nil, id, err
	}
	dv, err := decode[DatasetData](r.Body)
	if err != nil {
		return 0, nil, id, err
	}
	if dv.GovernanceRunID != "" {
		_, q, _, _, err := s.governanceOutputReady(tx.Statement.Context, tx, r)
		if err != nil {
			return 0, nil, id, err
		}
		return 200, encode(q), id, nil
	}
	obj, err := get(tx, "object", dv.Definition.SourceObjectID, false)
	if err != nil {
		return 0, nil, id, err
	}
	names := []string{}
	for _, f := range dv.Definition.Fields {
		names = append(names, f.Name)
	}
	if err = requireProcessingAuthorization(tx, p, Resource{Type: "raw_object", ID: obj.ID, Version: obj.Version}, names, "数据治理"); err != nil {
		return 0, nil, id, err
	}
	data, q, _, e := s.assessQuality(tx.Statement.Context, tx, r)
	if e != nil {
		var pe *Problem
		if !asProblem(e, &pe) {
			return 0, nil, id, e
		}
		data.QualityStatus = "blocked"
		q.Status = "blocked"
		q.FailureReasons = []Failure{{Code: pe.Code, Count: 1}}
		data.Quality = encode(q)
	}
	r.Body = encode(data)
	if err := save(tx, &r); err != nil {
		return 0, nil, id, err
	}
	return 200, encode(q), id, nil
}
func (s *Server) publishDataset(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if e := governanceDatasetWorkspaceWrite(tx, p, id); e != nil {
		return 0, nil, id, e
	}
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	r, e := get(tx, "dataset", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: "dataset", ID: r.ID}, "manage_resource"); err != nil {
		return 0, nil, id, err
	}
	if r.State == "published" {
		return 0, nil, id, conflict
	}
	if e = requireDatasetProcessingBasis(tx, r, p); e != nil {
		return 0, nil, id, e
	}
	data, q, _, e := s.assessQuality(tx.Statement.Context, tx, r)
	if e != nil {
		return 0, nil, id, e
	}
	if q.Status != "passed" {
		return 0, nil, id, problem(409, "QUALITY_BLOCKED", "阻断级质量规则未通过")
	}
	r.State = "published"
	r.Body = encode(data)
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 200, out, id, e
}
func (s *Server) withdrawDataset(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if e := governanceDatasetWorkspaceWrite(tx, p, id); e != nil {
		return 0, nil, id, e
	}
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	r, e := get(tx, "dataset", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: "dataset", ID: r.ID}, "manage_resource"); err != nil {
		return 0, nil, id, err
	}
	if r.State != "published" {
		return 0, nil, id, conflict
	}
	r.State = "withdrawn"
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 200, out, id, e
}
func asProblem(e error, p **Problem) bool {
	if v, ok := e.(*Problem); ok {
		*p = v
		return true
	}
	return false
}
func (s *Server) quality(c *gin.Context) {
	if e := governanceDatasetWorkspaceRead(s.db.WithContext(c.Request.Context()), c.Param("id")); e != nil {
		s.fail(c, e)
		return
	}
	r, e := get(s.db.WithContext(c.Request.Context()), "dataset", c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	allowed, readErr := governanceDatasetCanRead(s.db.WithContext(c.Request.Context()), principal(c), r)
	if readErr != nil {
		s.fail(c, readErr)
		return
	}
	if !allowed {
		s.fail(c, denied)
		return
	}
	data, e := decode[DatasetData](r.Body)
	if e != nil {
		s.fail(c, e)
		return
	}
	if data.GovernanceRunID != "" {
		_, q, _, _, err := s.governanceOutputReady(c.Request.Context(), s.db.WithContext(c.Request.Context()), r)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, q)
		return
	}
	if string(data.Quality) == "null" || len(data.Quality) == 0 {
		c.JSON(200, Quality{DatasetID: r.ID, ContractVersion: r.Version, RuleVersion: data.RuleVersion, Status: "not_checked", CheckedRange: encode(gin.H{}), FailureReasons: []Failure{}})
		return
	}
	c.Data(200, "application/json", data.Quality)
}
func (s *Server) lineage(c *gin.Context) {
	if e := governanceDatasetWorkspaceRead(s.db.WithContext(c.Request.Context()), c.Param("id")); e != nil {
		s.fail(c, e)
		return
	}
	r, e := get(s.db.WithContext(c.Request.Context()), "dataset", c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	allowed, readErr := governanceDatasetCanRead(s.db.WithContext(c.Request.Context()), principal(c), r)
	if readErr != nil {
		s.fail(c, readErr)
		return
	}
	if !allowed {
		s.fail(c, denied)
		return
	}
	data, e := decode[DatasetData](r.Body)
	if e != nil {
		s.fail(c, e)
		return
	}
	if data.GovernanceRunID != "" {
		_, _, run, report, err := s.governanceOutputReady(c.Request.Context(), s.db.WithContext(c.Request.Context()), r)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, gin.H{"dataset_id": r.ID, "contract_version": r.Version, "run_id": run.ID, "flow": report.Flow, "model": report.Model, "fields": report.Lineage, "nodes": report.Nodes})
		return
	}
	obj, e := get(s.db, "object", r.ParentID, false)
	if e != nil {
		s.fail(c, e)
		return
	}
	var task Record
	if e = s.db.Where("kind='task' AND parent_id=?", obj.ParentID).Take(&task).Error; e != nil {
		s.fail(c, e)
		return
	}
	v, e := decode[TaskData](task.Body)
	if e != nil {
		s.fail(c, e)
		return
	}
	archives := []string{}
	if v.ArchiveID != "" {
		archives = append(archives, v.ArchiveID)
	}
	c.JSON(200, gin.H{"dataset_id": r.ID, "contract_version": r.Version, "source_object_id": obj.ID, "task_id": task.ID, "run_id": v.RunID, "rule_version": data.RuleVersion, "rule_id": data.RuleID, "archive_manifests": archives})
}

func (s *Server) assessQuality(ctx context.Context, tx *gorm.DB, r Record) (DatasetData, Quality, []DataRow, error) {
	data, e := decode[DatasetData](r.Body)
	if e != nil {
		return data, Quality{}, nil, e
	}
	if data.GovernanceRunID != "" {
		ready, quality, _, _, err := s.governanceOutputReady(ctx, tx, r)
		return ready, quality, nil, err
	}
	if data.ResultBinding != nil {
		data, q, _, e := s.governedReady(ctx, tx, r)
		return data, q, nil, e
	}
	return s.assess(ctx, tx, r)
}
