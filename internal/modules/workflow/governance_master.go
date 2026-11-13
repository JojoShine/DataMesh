package workflow

import (
	"context"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"sort"
	"strings"
	"time"
)

type governanceMasterExecution struct {
	Master     GovernanceRef              `json:"master"`
	Digest     string                     `json:"digest"`
	Definition GovernanceMaster           `json:"definition"`
	Inputs     map[string]governanceInput `json:"inputs"`
}
type governanceMasterRow struct {
	Source   string  `json:"source"`
	RecordID string  `json:"record_id"`
	Ordinal  int64   `json:"ordinal"`
	Values   DataRow `json:"values"`
}
type GovernanceMasterCandidate struct {
	ID        string          `json:"id" gorm:"primaryKey"`
	RunID     string          `json:"run_id"`
	EntityID  string          `json:"entity_id"`
	Payload   json.RawMessage `json:"payload" gorm:"type:jsonb"`
	Conflicts json.RawMessage `json:"conflicts" gorm:"type:jsonb"`
	CreatedAt time.Time       `json:"created_at"`
}

func (GovernanceMasterCandidate) TableName() string { return "governance_master_candidates" }

type GovernanceMasterEntity struct {
	EntityID      string    `json:"entity_id" gorm:"primaryKey"`
	WorkspaceID   string    `json:"workspace_id"`
	MasterID      string    `json:"master_id"`
	LatestVersion int       `json:"latest_version"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (GovernanceMasterEntity) TableName() string { return "governance_master_entities" }

type GovernanceMasterRevision struct {
	EntityID    string          `json:"entity_id" gorm:"primaryKey"`
	Version     int             `json:"version" gorm:"primaryKey"`
	CandidateID string          `json:"candidate_id"`
	Choices     json.RawMessage `json:"choices" gorm:"type:jsonb"`
	Payload     json.RawMessage `json:"payload" gorm:"type:jsonb"`
	ActorID     string          `json:"actor_id"`
	Reason      string          `json:"reason"`
	CreatedAt   time.Time       `json:"created_at"`
}

func (GovernanceMasterRevision) TableName() string { return "governance_master_revisions" }
func (s *Server) authorizeMaster(ctx context.Context, p identity.Principal, def governanceMasterExecution) error {
	ctx = governanceReadContext(ctx)
	for _, input := range def.Inputs {
		if e := s.governanceCheckInput(ctx, p, input, true); e != nil {
			return e
		}
	}
	head, row, e := governanceRead(s.db.WithContext(ctx), p, "master", def.Master.ID, def.Master.Version)
	if e != nil {
		return e
	}
	if _, e = governanceWorkspaceRead(s.db.WithContext(ctx), p, workspaceID(head.WorkspaceID), false); e != nil {
		return e
	}
	if row.State != "published" || fingerprint(row.Definition) != def.Digest {
		return conflict
	}
	return nil
}
func (s *Server) enqueueMaster(tx *gorm.DB, p identity.Principal, ref GovernanceRef) (int, json.RawMessage, string, error) {
	head, row, e := governanceRead(tx, p, "master", ref.ID, ref.Version)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if row.State != "published" {
		return 0, nil, ref.ID, conflict
	}
	if _, e = governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID)); e != nil {
		return 0, nil, ref.ID, e
	}
	definition, e := decode[GovernanceDefinition](row.Definition)
	if e != nil || definition.Master == nil {
		return 0, nil, ref.ID, invalid
	}
	if e = s.capabilityReferences(tx, p, workspaceID(head.WorkspaceID), &definition); e != nil {
		return 0, nil, ref.ID, e
	}
	def := governanceMasterExecution{Master: ref, Digest: fingerprint(row.Definition), Definition: *definition.Master, Inputs: map[string]governanceInput{}}
	for _, source := range def.Definition.Sources {
		names := []string{source.RecordID}
		for _, field := range def.Definition.Model.Fields {
			if !slices.Contains(names, source.Mapping[field.Name]) {
				names = append(names, source.Mapping[field.Name])
			}
		}
		basis, e := s.governanceInputBasis(tx, p, source.Resource, names, true)
		if e != nil {
			return 0, nil, ref.ID, e
		}
		def.Inputs[source.ID] = basis
	}
	if e = governanceLock(tx, head.DepartmentID); e != nil {
		return 0, nil, ref.ID, e
	}
	var pending int64
	if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND state IN ('queued','running')", p.UserID).Count(&pending).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	if pending >= 10 {
		return 0, nil, ref.ID, conflict
	}
	id, e := newID()
	if e != nil {
		return 0, nil, ref.ID, e
	}
	run := GovernanceRun{ID: id, WorkspaceID: workspaceID(head.WorkspaceID), Kind: "master", DepartmentID: head.DepartmentID, ActorID: p.UserID, Definition: encode(def), State: "queued", Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&run).Error; e == nil {
		e = governanceRunEvent(tx, run, "MASTER_QUEUED")
	}
	return 201, encode(run), id, e
}
func masterConflicts(rows []governanceMasterRow, fields []GovernanceField) []string {
	out := []string{}
	for _, field := range fields {
		seen := map[string]bool{}
		for _, row := range rows {
			seen[string(encode(row.Values[field.Name]))] = true
		}
		if len(seen) > 1 {
			out = append(out, field.Name)
		}
	}
	return out
}
func (s *Server) executeMaster(ctx context.Context, p identity.Principal, run GovernanceRun, def governanceMasterExecution, guard func() error) (json.RawMessage, int64, error) {
	groups := map[string][]governanceMasterRow{}
	var processed, bytes int64
	var missing int64
	for _, source := range def.Definition.Sources {
		input := def.Inputs[source.ID]
		seenIDs := map[string]bool{}
		var ordinal int64
		total, e := s.governanceReadInput(ctx, input, s.etl.MaxRows+1, func(values []interface{}) error {
			if ordinal%250 == 0 {
				if e := guard(); e != nil {
					return e
				}
			}
			ordinal++
			processed++
			bytes += etlValueBytes(values)
			if processed > int64(s.etl.MaxRows) || bytes > s.etl.MaxBytes {
				return problem(409, "GOVERNANCE_LIMIT", "主数据候选总量超过治理上限，请缩小输入范围")
			}
			row := DataRow{}
			for i, field := range input.Fields {
				if i >= len(values) {
					return invalid
				}
				value := values[i]
				if field.Sensitive && value != nil {
					value = "***"
				}
				row[field.Name] = encode(value)
			}
			idField := slices.IndexFunc(input.Fields, func(field Field) bool { return field.Name == source.RecordID })
			if idField < 0 {
				return invalid
			}
			if e := normalizeSnapshotNumbers(row, Object{Fields: []Field{input.Fields[idField]}}); e != nil {
				return e
			}
			record := row[source.RecordID]
			if len(record) == 0 || string(record) == "null" || string(record) == `""` {
				return problem(409, "MASTER_SOURCE_ID_REQUIRED", "来源编号存在空值，请先进行质量处理")
			}
			rid := string(record)
			if seenIDs[rid] {
				return problem(409, "MASTER_SOURCE_ID_DUPLICATE", "来源编号不唯一，请先处理重复记录")
			}
			seenIDs[rid] = true
			mapped := DataRow{}
			for _, field := range def.Definition.Model.Fields {
				mapped[field.Name] = row[source.Mapping[field.Name]]
			}
			key, missingKey, e := masterMatchKey(def.Definition, mapped)
			if e != nil {
				return e
			}
			if missingKey {
				missing++
				return nil
			}
			entityID := fingerprint(encode([]interface{}{run.WorkspaceID, def.Master.ID, key}))
			groups[entityID] = append(groups[entityID], governanceMasterRow{Source: source.ID, RecordID: flowLiteralText(record), Ordinal: ordinal, Values: mapped})
			return nil
		})
		if e != nil {
			return nil, processed, e
		}
		if total != ordinal {
			return nil, processed, problem(409, "MASTER_INCOMPLETE_INPUT", "来源未完整读取，不能建立匹配候选")
		}
	}
	if e := guard(); e != nil {
		return nil, processed, e
	}
	conflicts := 0
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lease GovernanceRun
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND state='running' AND lease_owner=? AND lease_expires_at>now() AND NOT cancel_requested", run.ID, run.LeaseOwner).Take(&lease).Error; e != nil {
			return e
		}
		for _, entity := range ids {
			rows := groups[entity]
			bad := masterConflicts(rows, def.Definition.Model.Fields)
			if len(bad) > 0 {
				conflicts++
			}
			id, e := newID()
			if e != nil {
				return e
			}
			candidate := GovernanceMasterCandidate{ID: id, RunID: run.ID, EntityID: entity, Payload: encode(rows), Conflicts: encode(bad)}
			if e = tx.Create(&candidate).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return nil, processed, e
	}
	return encode(gin.H{"master": def.Master, "input_rows": processed, "entities": len(groups), "conflicting_entities": conflicts, "missing_key_rows": missing, "coverage": "full", "snapshot_kind": "sequential_sources"}), processed, nil
}
func (s *Server) masterRunAccess(tx *gorm.DB, p identity.Principal, id string) (GovernanceRun, governanceMasterExecution, error) {
	run, e := governanceRunRead(tx, p, id, false)
	if e != nil {
		return run, governanceMasterExecution{}, e
	}
	if run.Kind != "master" || run.State != "succeeded" {
		return run, governanceMasterExecution{}, conflict
	}
	def, e := decode[governanceMasterExecution](run.Definition)
	if e != nil {
		return run, def, e
	}
	if e = s.authorizeMaster(tx.Statement.Context, p, def); e != nil {
		return run, def, e
	}
	return run, def, nil
}
func (s *Server) registerGovernanceMaster(api, write *gin.RouterGroup) {
	s.registerMasterSnapshots(write)
	s.registerMasterBatch(write)
	write.POST("/governance/masters/:id/match", s.require("governance", "governor"), s.mutatePath("master_match", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			Version int `json:"version"`
		}](b)
		if e != nil {
			return 0, nil, id, e
		}
		return s.enqueueMaster(tx, p, GovernanceRef{ID: id, Version: input.Version})
	}))
	api.GET("/governance/master-runs/:id/candidates", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		_, def, e := s.masterRunAccess(tx, principal(c), c.Param("id"))
		if e != nil {
			s.fail(c, e)
			return
		}
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 15, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		q := tx.Model(&GovernanceMasterCandidate{}).Where("run_id=?", c.Param("id"))
		switch c.Query("conflicts") {
		case "", "false":
		case "true":
			q = q.Where("jsonb_array_length(conflicts)>0")
		default:
			s.fail(c, invalid)
			return
		}
		switch c.Query("confirmed") {
		case "":
		case "true":
			q = q.Where("EXISTS (SELECT 1 FROM governance_master_revisions r WHERE r.candidate_id=governance_master_candidates.id)")
		case "false":
			q = q.Where("NOT EXISTS (SELECT 1 FROM governance_master_revisions r WHERE r.candidate_id=governance_master_candidates.id)")
		default:
			s.fail(c, invalid)
			return
		}
		var total int64
		items := []struct {
			GovernanceMasterCandidate
			Confirmed bool `json:"confirmed"`
		}{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Select("governance_master_candidates.*, EXISTS(SELECT 1 FROM governance_master_revisions r WHERE r.candidate_id=governance_master_candidates.id) AS confirmed").Order("entity_id,id").Limit(size).Offset((page - 1) * size).Find(&items).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		labels := map[string]string{}
		if def.Definition.ModelRef != nil {
			ref := *def.Definition.ModelRef
			head, _, err := governanceRead(tx, principal(c), "model", ref.ID, ref.Version)
			if err != nil {
				s.fail(c, err)
				return
			}
			labels, err = s.governanceModelLabels(tx, principal(c), head, ref.Version, def.Definition.Model)
			if err != nil {
				s.fail(c, err)
				return
			}
		}
		for _, field := range def.Definition.Model.Fields {
			if field.BusinessName != "" {
				labels[field.Name] = field.BusinessName
			} else if field.Description != "" {
				labels[field.Name] = field.Description
			}
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size, "field_labels": labels})
	})
	api.GET("/governance/master-candidates/:id/history", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		var candidate GovernanceMasterCandidate
		if e := tx.Where("id=?", c.Param("id")).Take(&candidate).Error; e != nil {
			s.fail(c, e)
			return
		}
		if _, _, e := s.masterRunAccess(tx, principal(c), candidate.RunID); e != nil {
			s.fail(c, e)
			return
		}
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 15, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		var total int64
		rows := []GovernanceMasterRevision{}
		q := tx.Model(&GovernanceMasterRevision{}).Where("entity_id=?", candidate.EntityID)
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("version DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		for _, revision := range rows {
			var previous GovernanceMasterCandidate
			if e = tx.Where("id=?", revision.CandidateID).Take(&previous).Error; e != nil {
				s.fail(c, e)
				return
			}
			if _, _, e = s.masterRunAccess(tx, principal(c), previous.RunID); e != nil {
				s.fail(c, e)
				return
			}
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/master-candidates/:id/confirm", s.require("governance", "governor"), s.mutatePath("master_confirm", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[governanceMasterConfirmInput](b)
		if e != nil || !valid(input.Reason, 4000) || input.ExpectedVersion < 0 {
			return 0, nil, id, invalid
		}
		return s.confirmMasterCandidate(tx, p, id, input)
	}))
}

type governanceMasterConfirmInput struct {
	ExpectedVersion int            `json:"expected_version"`
	Choices         map[string]int `json:"choices"`
	Reason          string         `json:"reason"`
}

func (s *Server) confirmMasterCandidate(tx *gorm.DB, p identity.Principal, id string, input governanceMasterConfirmInput) (int, json.RawMessage, string, error) {
	var e error
	var candidate GovernanceMasterCandidate
	if e = tx.Where("id=?", id).Take(&candidate).Error; e != nil {
		return 0, nil, id, e
	}
	run, def, e := s.masterRunAccess(tx, p, candidate.RunID)
	if e != nil {
		return 0, nil, id, e
	}
	if _, e = governanceWorkspaceWrite(tx, p, run.WorkspaceID); e != nil {
		return 0, nil, id, e
	}
	rows, e := decode[[]governanceMasterRow](candidate.Payload)
	if e != nil {
		return 0, nil, id, e
	}
	payload, e := masterSelection(rows, def.Definition, input.Choices)
	if e != nil {
		return 0, nil, id, e
	}
	checkedModel := def.Definition.Model
	checkedModel.Fields = nil
	standards := map[GovernanceRef]GovernanceStandard{}
	for _, field := range def.Definition.Model.Fields {
		if field.Sensitive {
			continue
		}
		checkedModel.Fields = append(checkedModel.Fields, field)
		if field.Standard != nil {
			_, standard, e := governanceRead(tx, p, "standard", field.Standard.ID, field.Standard.Version)
			if e != nil {
				return 0, nil, id, e
			}
			if standard.State != "published" {
				return 0, nil, id, conflict
			}
			definition, e := decode[GovernanceDefinition](standard.Definition)
			if e != nil || definition.Standard == nil {
				return 0, nil, id, invalid
			}
			standards[*field.Standard] = *definition.Standard
		}
	}
	checkedPayload := DataRow{}
	for _, field := range checkedModel.Fields {
		checkedPayload[field.Name] = payload[field.Name]
	}
	if e = validateGovernanceOutputRow(checkedPayload, checkedModel, standards); e != nil {
		return 0, nil, id, e
	}
	for _, field := range checkedModel.Fields {
		payload[field.Name] = checkedPayload[field.Name]
	}
	if e = governanceLock(tx, run.DepartmentID); e != nil {
		return 0, nil, id, e
	}
	entity := GovernanceMasterEntity{EntityID: candidate.EntityID, WorkspaceID: run.WorkspaceID, MasterID: def.Master.ID, LatestVersion: 1}
	if input.ExpectedVersion == 0 {
		var count int64
		if e = tx.Model(&GovernanceMasterEntity{}).Where("entity_id=?", candidate.EntityID).Count(&count).Error; e != nil {
			return 0, nil, id, e
		}
		if count > 0 {
			return 0, nil, id, conflict
		}
		if e = tx.Create(&entity).Error; e != nil {
			return 0, nil, id, e
		}
	} else {
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("entity_id=?", candidate.EntityID).Take(&entity).Error; e != nil {
			return 0, nil, id, e
		}
		if entity.LatestVersion != input.ExpectedVersion {
			return 0, nil, id, conflict
		}
		entity.LatestVersion++
		if e = tx.Save(&entity).Error; e != nil {
			return 0, nil, id, e
		}
	}
	revision := GovernanceMasterRevision{EntityID: entity.EntityID, Version: entity.LatestVersion, CandidateID: id, Choices: encode(input.Choices), Payload: encode(payload), ActorID: p.UserID, Reason: input.Reason}
	e = tx.Create(&revision).Error
	return 201, encode(revision), id, e
}

func masterSelection(rows []governanceMasterRow, def GovernanceMaster, choices map[string]int) (DataRow, error) {
	if len(choices) != len(def.Model.Fields) || len(rows) == 0 {
		return nil, invalid
	}
	result := DataRow{}
	for _, field := range def.Model.Fields {
		index, ok := choices[field.Name]
		if !ok || index < 0 || index >= len(rows) {
			return nil, invalid
		}
		result[field.Name] = rows[index].Values[field.Name]
	}
	return result, nil
}

func masterMatchKey(def GovernanceMaster, values DataRow) ([]json.RawMessage, bool, error) {
	key := []json.RawMessage{}
	for _, name := range def.Keys {
		raw := values[name]
		if len(raw) == 0 || string(raw) == "null" || strings.TrimSpace(flowLiteralText(raw)) == "" {
			return nil, true, nil
		}
		index := slices.IndexFunc(def.Model.Fields, func(field GovernanceField) bool { return field.Name == name })
		if index < 0 {
			return nil, false, invalid
		}
		field := def.Model.Fields[index]
		field.Standard = nil
		field.Nullable = false
		normalized := DataRow{name: raw}
		if e := validateGovernanceOutputRow(normalized, GovernanceModel{Fields: []GovernanceField{field}}, nil); e != nil {
			return nil, false, e
		}
		values[name] = normalized[name]
		key = append(key, normalized[name])
	}
	return key, false, nil
}
