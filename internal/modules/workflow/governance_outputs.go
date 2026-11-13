package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The proof row has no UPDATE/DELETE grant. Dataset publication changes only its lifecycle state.
func (s *Server) registerGovernanceOutput(ctx context.Context, tx *sql.Tx, run GovernanceRun, def governanceFlowExecution, model GovernanceModel, report governanceFlowReport) error {
	provenance, err := governanceOutputProvenance(ctx, tx, def)
	if err != nil {
		return err
	}
	quality := Quality{DatasetID: run.ID, ContractVersion: 1, RuleVersion: report.Model.Version, Status: "passed", Passed: int(report.OutputRows), Failed: 0, CheckedRange: encode(report), FailureReasons: []Failure{}}
	data := DatasetData{DataLayer: report.DataLayer, Storage: report.Storage, Governance: provenance, GovernanceRunID: run.ID, Definition: DatasetInput{Name: def.Pinned[0].Definition.Name, Description: def.Pinned[0].Definition.Description, OwnerID: run.ActorID, Fields: governanceModelFields(model), UsageRestrictions: model.Contract.UsageRestrictions}, RuleVersion: report.Model.Version, QualityStatus: "passed", CompletenessStatus: "complete", LatestWatermark: encode(map[string]interface{}{"governance_run_id": run.ID, "model": report.Model}), Quality: encode(quality)}
	if provenance.SingleSource && provenance.Source != nil {
		data.Definition.SourceObjectID = provenance.Source.ID
	}
	body := string(encode(data))
	if _, err := tx.ExecContext(ctx, "INSERT INTO business_records(kind,id,department_id,owner_id,parent_id,state,version,body) VALUES('dataset',$1,$2,$3,$4,'draft',1,$5::jsonb)", run.ID, governanceOutputDepartment(run.DepartmentID, provenance), run.ActorID, def.Flow.ID, body); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO governance_outputs(run_id,dataset_id,dataset_body) VALUES($1,$1,$2::jsonb)", run.ID, body)
	return err
}

func (s *Server) registerGovernanceOutputs(api *gin.RouterGroup) {
	api.GET("/governance/runs/:id/output", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		run, err := governanceRunRead(tx, principal(c), c.Param("id"), false)
		if err != nil {
			s.fail(c, err)
			return
		}
		var proof struct {
			DatasetID   string
			DatasetBody json.RawMessage
			CreatedAt   time.Time
		}
		if err = tx.Table("governance_outputs").Where("run_id=?", run.ID).Take(&proof).Error; err != nil {
			s.fail(c, err)
			return
		}
		record, err := get(tx, "dataset", proof.DatasetID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		// Historical evidence remains readable even when freshness or authorization blocks new delivery.
		var report governanceFlowReport
		if err = json.Unmarshal(run.Result, &report); err != nil {
			s.fail(c, err)
			return
		}
		var mounts []Record
		if err = tx.Where("kind='mount_revision' AND body->'resource'->>'resource_id'=?", record.ID).Find(&mounts).Error; err != nil {
			s.fail(c, err)
			return
		}
		downstream := []string{}
		for _, mount := range mounts {
			downstream = append(downstream, mount.ParentID)
		}
		c.JSON(200, gin.H{"dataset_id": record.ID, "contract_version": record.Version, "publication_status": record.State, "governance_run_id": run.ID, "data": json.RawMessage(proof.DatasetBody), "evidence": report, "downstream_mount_ids": downstream, "created_at": proof.CreatedAt})
	})
}

type governanceLineageDepthKey struct{}

func (s *Server) governanceOutputReady(ctx context.Context, tx *gorm.DB, record Record) (DatasetData, Quality, GovernanceRun, governanceFlowReport, error) {
	depth, _ := ctx.Value(governanceLineageDepthKey{}).(int)
	if depth >= 32 {
		return DatasetData{}, Quality{}, GovernanceRun{}, governanceFlowReport{}, problem(409, "GOVERNANCE_LINEAGE_DEPTH", "连续治理成果来源超过32层，请缩短计算链路")
	}
	ctx = context.WithValue(ctx, governanceLineageDepthKey{}, depth+1)
	tx = tx.WithContext(ctx)
	var purged int64
	if e := tx.Table("governance_snapshot_purges").Where("run_id=?", record.ID).Count(&purged).Error; e != nil {
		return DatasetData{}, Quality{}, GovernanceRun{}, governanceFlowReport{}, e
	}
	if purged > 0 {
		return DatasetData{}, Quality{}, GovernanceRun{}, governanceFlowReport{}, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "成果快照已按保留策略清理，历史证据仍保留")
	}
	data, err := decode[DatasetData](record.Body)
	var run GovernanceRun
	var report governanceFlowReport
	var quality Quality
	fail := func(e error) (DatasetData, Quality, GovernanceRun, governanceFlowReport, error) {
		return data, quality, run, report, e
	}
	if err != nil {
		return fail(err)
	}
	var proof struct {
		DatasetID   string
		DatasetBody json.RawMessage
	}
	if err = tx.Table("governance_outputs").Where("run_id=? AND dataset_id=?", data.GovernanceRunID, record.ID).Take(&proof).Error; err != nil {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果没有可信执行登记证据"))
	}
	proofData, proofErr := decode[DatasetData](proof.DatasetBody)
	if proofErr != nil {
		return fail(proofErr)
	}
	if fingerprint(encode(data)) != fingerprint(encode(proofData)) {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果契约与不可变登记证据不同"))
	}
	if err = tx.Where("id=? AND kind='dataflow' AND state='succeeded'", data.GovernanceRunID).Take(&run).Error; err != nil {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果运行尚未成功提交"))
	}
	if run.ActorID != record.OwnerID || governanceOutputDepartment(run.DepartmentID, proofData.Governance) != record.DepartmentID || record.Version != 1 {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果归属或版本与执行证据不符"))
	}
	if report, err = decode[governanceFlowReport](run.Result); err != nil {
		return fail(err)
	}
	if report.DatasetID != record.ID {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果运行引用不一致"))
	}
	def, err := decode[governanceFlowExecution](run.Definition)
	if err != nil {
		return fail(err)
	}
	actor, err := s.etlPrincipal(ctx, run.ActorID)
	if err != nil {
		return fail(denied)
	}
	if err = s.governanceFlowAuthorize(ctx, actor, def); err != nil {
		return fail(err)
	}
	var model *GovernanceModel
	for _, pinned := range def.Pinned {
		if pinned.Kind == "model" && pinned.Ref == report.Model {
			model = pinned.Definition.Model
		}
	}
	if model == nil || run.FinishedAt == nil {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果缺少模型或完成证据"))
	}
	if model.Contract.FreshnessSeconds > 0 && time.Now().After(run.FinishedAt.Add(time.Duration(model.Contract.FreshnessSeconds)*time.Second)) {
		return fail(problem(409, "GOVERNANCE_OUTPUT_STALE", "这份数据已超过约定的使用时间，暂时不能使用或发布。请到“数据流设计”重新运行对应的数据流，生成新结果后再试。"))
	}
	var count int64
	if report.Storage != nil {
		if fingerprint(encode(data.Storage)) != fingerprint(encode(report.Storage)) {
			return fail(invalid)
		}
		count, err = dorisResultCount(ctx, report.Storage, run.ID)
	} else {
		err = tx.Table("governance_flow_rows").Where("run_id=? AND node_id=? AND port=''", run.ID, report.OutputNode).Count(&count).Error
	}
	if err != nil {
		return fail(err)
	}
	if count != report.OutputRows {
		return fail(problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果快照数量与提交证据不一致"))
	}
	quality, err = decode[Quality](data.Quality)
	return data, quality, run, report, err
}

func (s *Server) governanceOutputPage(ctx context.Context, tx *gorm.DB, record Record, start, size int) ([]DataRow, error) {
	_, _, run, report, err := s.governanceOutputReady(ctx, tx, record)
	if err != nil {
		return nil, err
	}
	if report.Storage != nil {
		data, e := decode[DatasetData](record.Body)
		if e != nil {
			return nil, e
		}
		rows, e := dorisResultPage(ctx, report.Storage, run.ID, data.Definition.Fields, start, size)
		if e != nil {
			return nil, e
		}
		if _, _, _, _, e = s.governanceOutputReady(ctx, tx, record); e != nil {
			return nil, e
		}
		return rows, nil
	}
	var encrypted []struct {
		Ordinal       int64
		EncryptedBody []byte
	}
	if err = tx.Table("governance_flow_rows").Where("run_id=? AND node_id=? AND port='' AND ordinal>?", run.ID, report.OutputNode, start).Order("ordinal").Limit(size).Find(&encrypted).Error; err != nil {
		return nil, err
	}
	out := []DataRow{}
	for _, row := range encrypted {
		plain, e := s.runtime.cryptScope(row.EncryptedBody, false, governanceFlowScope(run.ID, report.OutputNode, "", row.Ordinal))
		if e != nil {
			return nil, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果快照无法验证")
		}
		decoded, e := decode[DataRow](plain)
		if e != nil {
			return nil, e
		}
		out = append(out, decoded)
	}
	if int64(start) > report.OutputRows || len(out) != min(size, int(report.OutputRows)-start) {
		return nil, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果分页数量不一致")
	}
	if _, _, _, _, err = s.governanceOutputReady(ctx, tx, record); err != nil {
		return nil, err
	}
	return out, nil
}

func requireGovernanceOutputSharingBasis(tx *gorm.DB, record Record, data DatasetData) error {
	var run GovernanceRun
	if err := tx.Where("id=? AND actor_id=? AND state='succeeded' AND kind='dataflow'", data.GovernanceRunID, record.OwnerID).Take(&run).Error; err != nil {
		return problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果运行证据不可用")
	}
	def, err := decode[governanceFlowExecution](run.Definition)
	if err != nil {
		return err
	}
	var administrator int64
	if err = tx.Table("users u").Joins("JOIN user_roles ur ON ur.user_id=u.id").Where("u.id=? AND u.active=true AND ur.role='admin'", run.ActorID).Count(&administrator).Error; err != nil {
		return err
	}
	if administrator > 0 {
		return nil
	}
	var grants []Record
	if err = tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=?", run.ActorID).Find(&grants).Error; err != nil {
		return err
	}
	return requireGovernanceInputsSharingBasis(tx, run.ActorID, run.WorkspaceID, def.Inputs, grants, 0)
}
func requireGovernanceInputsSharingBasis(tx *gorm.DB, actorID, workspace string, inputs map[string]governanceInput, grants []Record, depth int) error {
	if depth >= 32 {
		return problem(409, "GOVERNANCE_LINEAGE_DEPTH", "共享依据追溯超过32层")
	}
	for _, input := range inputs {
		if input.MasterSnapshotID != "" {
			snapshot, e := get(tx, "master_snapshot", input.MasterSnapshotID, false)
			if e != nil {
				return e
			}
			body, e := decode[governanceMasterSnapshot](snapshot.Body)
			if e != nil {
				return e
			}
			if snapshot.OwnerID != actorID || body.WorkspaceID != workspace || snapshot.ID != input.Resource.ID || snapshot.Version != input.Resource.Version || fingerprint(snapshot.Body) != input.ResourceFingerprint || len(body.Runs) == 0 {
				return denied
			}
			for _, id := range body.Runs {
				var original GovernanceRun
				if e = tx.Where("id=? AND kind='master' AND state='succeeded' AND actor_id=? AND workspace_id=?", id, actorID, workspace).Take(&original).Error; e != nil {
					return e
				}
				master, e := decode[governanceMasterExecution](original.Definition)
				if e != nil {
					return e
				}
				if master.Master != body.Master || master.Digest != body.MasterDigest {
					return conflict
				}
				if e = requireGovernanceInputsSharingBasis(tx, actorID, workspace, master.Inputs, grants, depth+1); e != nil {
					return e
				}
			}
			continue
		}

		if input.WorkflowParent != "" {
			ancestor, e := get(tx, "dataset", input.Resource.ID, false)
			if e != nil {
				return e
			}
			ancestorData, e := decode[DatasetData](ancestor.Body)
			if e != nil {
				return e
			}
			if ancestor.OwnerID != actorID {
				return denied
			}
			if e = requireGovernanceOutputSharingBasis(tx, ancestor, ancestorData); e != nil {
				return e
			}
			continue
		}
		allowed := false
		for _, r := range grants {
			grant, e := decode[ProcessingAuthorization](r.Body)
			if e != nil {
				return e
			}
			if grant.Resource == input.Resource && grant.AllowDerived && grant.AllowSharing && valid(grant.SharingBasis, 2000) && grant.Purpose == "数据治理" && grant.ExpiresAt.After(time.Now()) && !slices.ContainsFunc(input.Fields, func(f Field) bool { return !slices.Contains(grant.Fields, f.Name) }) && fingerprint(encode([]interface{}{r.ID, r.Version, grant})) == input.ProcessingFingerprint {
				allowed = true
				break
			}
		}
		if !allowed {
			return problem(403, "DERIVED_SHARING_BASIS_REQUIRED", "治理成果缺少有效的来源加工及成果共享依据")
		}
	}
	return nil
}

func (s *Server) governanceOutputResourceReader(ctx context.Context, p identity.Principal, run *ETLRun, schema TableConfig, dataset Record) (func(func([]interface{}) error) error, func(), error) {
	_, _, _, report, err := s.governanceOutputReady(ctx, s.db.WithContext(ctx), dataset)
	if err != nil {
		return nil, nil, err
	}
	sensitive := map[string]bool{}
	for _, field := range schema.Fields {
		sensitive[field.Name] = field.Sensitive && !admin(p)
		if sensitive[field.Name] && field.PrimaryKey {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏主键不能用于批量交换")
		}
	}
	for _, mapping := range run.Definition.Mapping {
		if sensitive[mapping.Source] && slices.Contains(run.Definition.Target.Keys, mapping.Target) {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏字段不能作为目标主键")
		}
	}
	run.Planned = int(report.OutputRows)
	ref := *run.Definition.SourceResource
	run.InputSnapshot = &ETLInputSnapshot{Kind: ref.Type, ResourceID: ref.ID, ContractVersion: ref.Version, SnapshotID: dataset.ID, Fields: fieldNames(schema.Fields)}
	reader := func(consume func([]interface{}) error) error {
		for offset := 0; offset < run.Planned; offset += 250 {
			if e := s.etlInputIdentity(ctx, p); e != nil {
				return e
			}
			if e := s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization); e != nil {
				return e
			}
			rows, e := s.governanceOutputPage(ctx, s.db.WithContext(ctx), dataset, offset, min(250, run.Planned-offset))
			if e != nil {
				return e
			}
			for _, row := range rows {
				projected := rawProjection(row, fieldNames(schema.Fields), sensitive)
				values := make([]interface{}, len(run.Definition.Mapping))
				for i, m := range run.Definition.Mapping {
					decoder := json.NewDecoder(bytes.NewReader(projected[m.Source]))
					decoder.UseNumber()
					if e = decoder.Decode(&values[i]); e != nil {
						return e
					}
					if number, ok := values[i].(json.Number); ok {
						values[i] = number.String()
					}
					if values[i], e = transformETL(values[i], m.Rule); e != nil {
						return e
					}
				}
				if e = consume(values); e != nil {
					return e
				}
			}
		}
		return s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization)
	}
	return reader, func() {}, nil
}
