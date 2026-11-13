package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sort"
)

type governanceMasterBatchInput struct {
	CandidateIDs []string `json:"candidate_ids"`
	PreviewToken string   `json:"preview_token"`
	Reason       string   `json:"reason"`
}
type governanceMasterBatchItem struct {
	CandidateID     string                `json:"candidate_id"`
	EntityID        string                `json:"entity_id"`
	ExpectedVersion int                   `json:"expected_version"`
	Choices         map[string]int        `json:"choices"`
	Payload         DataRow               `json:"payload"`
	Sources         []governanceMasterRow `json:"sources"`
}
type governanceMasterBatchPreview struct {
	PreviewToken string                      `json:"preview_token"`
	Items        []governanceMasterBatchItem `json:"items"`
}

func masterBatchChoices(rows []governanceMasterRow, model GovernanceModel) (map[string]int, error) {
	if len(rows) == 0 || len(masterConflicts(rows, model.Fields)) > 0 {
		return nil, problem(409, "MASTER_BATCH_CONFLICT", "批量确认仅支持无字段冲突的候选，请逐字段核实冲突记录")
	}
	choices := map[string]int{}
	for _, field := range model.Fields {
		// Equal masked placeholders are not proof that the original sensitive values match.
		if len(rows) > 1 && field.Sensitive {
			for _, row := range rows {
				value := row.Values[field.Name]
				if len(value) > 0 && string(value) != "null" {
					return nil, problem(409, "MASTER_BATCH_MASKED_COMPARISON", "跨来源敏感值无法判同，请使用逐字段确认")
				}
			}
		}
		choices[field.Name] = 0
	}
	return choices, nil
}

func (s *Server) masterBatchPreview(tx *gorm.DB, p identity.Principal, runID string, ids []string) (governanceMasterBatchPreview, error) {
	result := governanceMasterBatchPreview{Items: []governanceMasterBatchItem{}}
	if len(ids) < 1 || len(ids) > 100 {
		return result, invalid
	}
	run, def, e := s.masterRunAccess(tx, p, runID)
	if e != nil {
		return result, e
	}
	if _, e = governanceWorkspaceWrite(tx, p, run.WorkspaceID); e != nil {
		return result, e
	}
	ids = append([]string{}, ids...)
	sort.Strings(ids)
	for i, id := range ids {
		if !valid(id, 128) || (i > 0 && ids[i-1] == id) {
			return result, invalid
		}
		var candidate GovernanceMasterCandidate
		if e = tx.Where("id=? AND run_id=?", id, runID).Take(&candidate).Error; e != nil {
			return result, e
		}
		var confirmed int64
		if e = tx.Model(&GovernanceMasterRevision{}).Where("candidate_id=?", id).Count(&confirmed).Error; e != nil {
			return result, e
		}
		if confirmed > 0 {
			return result, problem(409, "MASTER_BATCH_ALREADY_CONFIRMED", "所选候选已有确认记录，请刷新后重新选择")
		}
		rows, e := decode[[]governanceMasterRow](candidate.Payload)
		if e != nil {
			return result, e
		}
		choices, e := masterBatchChoices(rows, def.Definition.Model)
		if e != nil {
			return result, e
		}
		payload, e := masterSelection(rows, def.Definition, choices)
		if e != nil {
			return result, e
		}
		var version int
		if e = tx.Model(&GovernanceMasterEntity{}).Select("COALESCE(max(latest_version),0)").Where("entity_id=?", candidate.EntityID).Scan(&version).Error; e != nil {
			return result, e
		}
		// Existing entities require deliberate revision, even if this new candidate itself has no conflicts.
		if version > 0 {
			return result, problem(409, "MASTER_BATCH_EXISTING_ENTITY", "所选实体已有历史版本，请逐条核对后修订")
		}
		result.Items = append(result.Items, governanceMasterBatchItem{CandidateID: id, EntityID: candidate.EntityID, ExpectedVersion: version, Choices: choices, Payload: payload, Sources: rows})
	}
	result.PreviewToken = fingerprint(encode(struct {
		Actor      string
		Run        string
		Definition string
		Items      []governanceMasterBatchItem
	}{p.UserID, run.ID, fingerprint(run.Definition), result.Items}))
	return result, nil
}

func (s *Server) registerMasterBatch(write *gin.RouterGroup) {
	for _, operation := range []string{"preview", "confirm"} {
		action := operation
		write.POST("/governance/master-runs/:id/batch-"+action, s.require("governance", "governor"), s.mutatePath("master_batch_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			input, e := bind[governanceMasterBatchInput](b)
			if e != nil {
				return 0, nil, id, e
			}
			if action == "confirm" {
				if !valid(input.Reason, 4000) || !valid(input.PreviewToken, 64) {
					return 0, nil, id, invalid
				}
				run, _, err := s.masterRunAccess(tx, p, id)
				if err != nil {
					return 0, nil, id, err
				}
				// Match single confirmation's workspace -> department lock order.
				if _, e = governanceWorkspaceWrite(tx, p, run.WorkspaceID); e != nil {
					return 0, nil, id, e
				}
				if e = governanceLock(tx, run.DepartmentID); e != nil {
					return 0, nil, id, e
				}
			}
			preview, e := s.masterBatchPreview(tx, p, id, input.CandidateIDs)
			if e != nil {
				return 0, nil, id, e
			}
			if action == "preview" {
				return 200, encode(preview), id, nil
			}
			if input.PreviewToken != preview.PreviewToken {
				return 0, nil, id, problem(409, "MASTER_BATCH_PREVIEW_CHANGED", "预览依据已变化，请重新预览后确认")
			}
			revisions := []GovernanceMasterRevision{}
			for _, item := range preview.Items {
				_, raw, _, e := s.confirmMasterCandidate(tx, p, item.CandidateID, governanceMasterConfirmInput{ExpectedVersion: item.ExpectedVersion, Choices: item.Choices, Reason: input.Reason})
				if e != nil {
					return 0, nil, id, e
				}
				revision, e := decode[GovernanceMasterRevision](raw)
				if e != nil {
					return 0, nil, id, e
				}
				revisions = append(revisions, revision)
			}
			return 201, encode(gin.H{"items": revisions, "total": len(revisions)}), id, nil
		}))
	}
}
