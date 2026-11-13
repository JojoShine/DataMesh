package workflow

import (
	"bytes"
	"context"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"slices"
)

// A snapshot freezes confirmed revisions, while execution still rechecks every original grant.
type governanceMasterRevisionRef struct {
	EntityID    string `json:"entity_id"`
	Version     int    `json:"version"`
	CandidateID string `json:"candidate_id"`
}
type governanceMasterSnapshot struct {
	WorkspaceID  string                             `json:"workspace_id"`
	Master       GovernanceRef                      `json:"master"`
	MasterDigest string                             `json:"master_digest"`
	Model        GovernanceRef                      `json:"model"`
	Fields       []Field                            `json:"fields"`
	Revisions    []governanceMasterRevisionRef      `json:"revisions"`
	Runs         []string                           `json:"runs"`
	Origins      map[string][]GovernanceFieldOrigin `json:"origins"`
	Name         string                             `json:"name"`
}

func masterSnapshotRead(tx *gorm.DB, p identity.Principal, id string) (Record, governanceMasterSnapshot, error) {
	row, e := get(tx, "master_snapshot", id, false)
	if e != nil {
		return row, governanceMasterSnapshot{}, e
	}
	body, e := decode[governanceMasterSnapshot](row.Body)
	if e != nil {
		return row, body, e
	}
	if row.OwnerID != p.UserID || row.DepartmentID != p.DepartmentID {
		return row, body, denied
	}
	if e = governanceWorkspaceMatch(tx.Statement.Context, body.WorkspaceID); e != nil {
		return row, body, e
	}
	_, e = governanceWorkspaceRead(tx, p, body.WorkspaceID, false)
	return row, body, e
}
func (s *Server) masterSnapshotBasis(tx *gorm.DB, p identity.Principal, ref Resource, names []string, parameters map[string]string) (governanceInput, error) {
	out := governanceInput{}
	row, body, e := masterSnapshotRead(tx, p, ref.ID)
	if e != nil {
		return out, e
	}
	if ref.Type != "master_snapshot" || ref.Version != row.Version || len(parameters) > 0 || !validFields(names, body.Fields) {
		return out, invalid
	}
	_, master, e := governanceRead(tx, p, "master", body.Master.ID, body.Master.Version)
	if e != nil {
		return out, e
	}
	if master.State != "published" || fingerprint(master.Definition) != body.MasterDigest {
		return out, conflict
	}
	_, model, e := governanceRead(tx, p, "model", body.Model.ID, body.Model.Version)
	if e != nil {
		return out, e
	}
	if model.State != "published" {
		return out, conflict
	}
	for _, id := range body.Runs {
		run, def, e := s.masterRunAccess(tx, p, id)
		if e != nil {
			return out, e
		}
		if run.ActorID != p.UserID || run.WorkspaceID != body.WorkspaceID || def.Master != body.Master || def.Digest != body.MasterDigest {
			return out, denied
		}
	}
	out = governanceInput{MasterSnapshotID: row.ID, Resource: ref, Fields: []Field{}, DepartmentID: p.DepartmentID, Administrator: admin(p), ResourceFingerprint: fingerprint(row.Body), ProcessingFingerprint: body.MasterDigest, Origins: map[string][]GovernanceFieldOrigin{}}
	for _, name := range names {
		for _, field := range body.Fields {
			if field.Name == name {
				out.Fields = append(out.Fields, field)
				out.Origins[name] = body.Origins[name]
			}
		}
	}
	return out, nil
}
func (s *Server) readMasterSnapshot(ctx context.Context, basis governanceInput, limit int, consume func([]interface{}) error) (int64, error) {
	tx := s.db.WithContext(ctx)
	row, e := get(tx, "master_snapshot", basis.MasterSnapshotID, false)
	if e != nil {
		return 0, e
	}
	if basis.MasterSnapshotID != basis.Resource.ID || row.Version != basis.Resource.Version || fingerprint(row.Body) != basis.ResourceFingerprint {
		return 0, conflict
	}
	body, e := decode[governanceMasterSnapshot](row.Body)
	if e != nil {
		return 0, e
	}

	refs := body.Revisions[:min(limit, len(body.Revisions))]
	for start := 0; start < len(refs); start += 250 {
		batch := refs[start:min(start+250, len(refs))]
		tuples := make([][]interface{}, 0, len(batch))
		for _, ref := range batch {
			tuples = append(tuples, []interface{}{ref.EntityID, ref.Version, ref.CandidateID})
		}
		revisions := []GovernanceMasterRevision{}
		if e = tx.Where("(entity_id,version,candidate_id) IN ?", tuples).Find(&revisions).Error; e != nil {
			return 0, e
		}
		if len(revisions) != len(batch) {
			return 0, conflict
		}
		byEntity := map[string]GovernanceMasterRevision{}
		for _, revision := range revisions {
			byEntity[revision.EntityID] = revision
		}
		for _, ref := range batch {
			revision, ok := byEntity[ref.EntityID]
			if !ok || revision.Version != ref.Version || revision.CandidateID != ref.CandidateID {
				return 0, conflict
			}
			payload, e := decode[DataRow](revision.Payload)
			if e != nil {
				return 0, e
			}
			values := make([]interface{}, len(basis.Fields))
			for i, field := range basis.Fields {
				raw, ok := payload[field.Name]
				if !ok {
					return 0, invalid
				}
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if e = decoder.Decode(&values[i]); e != nil {
					return 0, e
				}
			}
			if e = consume(values); e != nil {
				return 0, e
			}
		}
	}

	return int64(len(body.Revisions)), nil
}
func (s *Server) registerMasterSnapshots(write *gin.RouterGroup) {
	write.POST("/governance/masters/:id/results", s.require("governance", "governor"), s.mutatePath("master_result_draft", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
		}](b)
		if e != nil || !valid(input.Name, 200) {
			return 0, nil, id, invalid
		}
		head, version, e := governanceRead(tx, p, "master", id, input.Version)
		if e != nil {
			return 0, nil, id, e
		}
		if version.State != "published" {
			return 0, nil, id, conflict
		}
		spaceID := workspaceID(head.WorkspaceID)
		if _, e = governanceWorkspaceWrite(tx, p, spaceID); e != nil {
			return 0, nil, id, e
		}
		if e = governanceLock(tx, head.DepartmentID); e != nil {
			return 0, nil, id, e
		}
		definition, e := decode[GovernanceDefinition](version.Definition)
		if e != nil || definition.Master == nil || definition.Master.ModelRef == nil {
			return 0, nil, id, problem(409, "MASTER_RESULT_MODEL_REQUIRED", "请先固定已发布实体模型后再形成成果")
		}
		master := *definition.Master
		body := governanceMasterSnapshot{WorkspaceID: spaceID, Master: GovernanceRef{ID: id, Version: input.Version}, MasterDigest: fingerprint(version.Definition), Model: *master.ModelRef, Fields: governanceModelFields(master.Model), Revisions: []governanceMasterRevisionRef{}, Runs: []string{}, Origins: map[string][]GovernanceFieldOrigin{}, Name: input.Name}
		// Select latest confirmations from this exact definition and this actor's original matches.
		revisions := []struct {
			EntityID      string
			Version       int
			CandidateID   string
			Choices       json.RawMessage
			RunID         string
			CandidateRows json.RawMessage
		}{}
		if e = tx.Table("governance_master_revisions v").Select("v.entity_id,v.version,v.candidate_id,v.choices,c.run_id,c.payload AS candidate_rows").Joins("JOIN governance_master_entities e ON e.entity_id=v.entity_id AND e.latest_version=v.version").Joins("JOIN governance_master_candidates c ON c.id=v.candidate_id").Joins("JOIN governance_runs r ON r.id=c.run_id").Where("e.workspace_id=? AND e.master_id=? AND r.actor_id=? AND r.definition->'master'->>'id'=? AND (r.definition->'master'->>'version')::integer=?", spaceID, id, p.UserID, id, input.Version).Order("v.entity_id").Limit(s.etl.MaxRows + 1).Find(&revisions).Error; e != nil {
			return 0, nil, id, e
		}
		if len(revisions) == 0 {
			return 0, nil, id, problem(409, "MASTER_RESULT_CONFIRMATION_REQUIRED", "当前版本尚无本人来源匹配后的已确认实体")
		}
		if len(revisions) > s.etl.MaxRows {
			return 0, nil, id, problem(409, "GOVERNANCE_LIMIT", "已确认实体超过当前单次治理上限")
		}

		for _, revision := range revisions {
			body.Revisions = append(body.Revisions, governanceMasterRevisionRef{revision.EntityID, revision.Version, revision.CandidateID})
			if !slices.Contains(body.Runs, revision.RunID) {
				body.Runs = append(body.Runs, revision.RunID)
			}
			choices, e := decode[map[string]int](revision.Choices)
			if e != nil {
				return 0, nil, id, e
			}
			rows, e := decode[[]governanceMasterRow](revision.CandidateRows)
			if e != nil {
				return 0, nil, id, e
			}
			for target, index := range choices {
				if index < 0 || index >= len(rows) {
					return 0, nil, id, invalid
				}
				sourceIndex := slices.IndexFunc(master.Sources, func(source GovernanceMasterSource) bool { return source.ID == rows[index].Source })
				if sourceIndex < 0 {
					return 0, nil, id, invalid
				}
				source := master.Sources[sourceIndex]
				origin := GovernanceFieldOrigin{Node: source.ID, Resource: source.Resource, Field: source.Mapping[target]}
				if !slices.Contains(body.Origins[target], origin) {
					body.Origins[target] = append(body.Origins[target], origin)
				}
			}
		}

		snapshot, e := newRecord("master_snapshot", p.DepartmentID, p.UserID, id, "active", body)
		if e != nil {
			return 0, nil, id, e
		}
		if e = tx.Create(&snapshot).Error; e != nil {
			return 0, nil, id, e
		}
		ref := Resource{Type: "master_snapshot", ID: snapshot.ID, Version: 1}
		if _, e = s.masterSnapshotBasis(tx, p, ref, fieldNames(body.Fields), nil); e != nil {
			return 0, nil, id, e
		}
		graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{{ID: "confirmed_entities", Kind: "source", Source: &GovernanceFlowSource{Resource: ref, Fields: fieldNames(body.Fields)}}, {ID: "result", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "confirmed_entities"}}, Model: master.ModelRef}}}
		status, flow, flowID, e := s.governanceSave(tx, p, "dataflow", "", encode(governanceWrite{Definition: GovernanceDefinition{OwnerID: p.UserID, StewardID: p.UserID, Name: input.Name, Description: "按已确认主数据实体版本形成成果；保留来源编号、确认版本与字段血缘。", Dataflow: &graph}, ChangeNote: "固定主数据确认版本并建立成果处理草稿"}))
		return status, encode(gin.H{"flow": flow, "snapshot_id": snapshot.ID, "entity_count": len(revisions)}), flowID, e
	}))
}
