package workflow

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) governanceWorkspaceOverview(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	space, e := governanceWorkspaceRead(tx, p, c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	var sources int64
	if e = tx.Model(&GovernanceWorkspaceSource{}).Where("workspace_id=? AND active", space.ID).Count(&sources).Error; e != nil {
		s.fail(c, e)
		return
	}
	var definitions []struct {
		Kind  string `json:"kind"`
		Count int64  `json:"count"`
	}
	if e = tx.Model(&GovernanceDefinitionRecord{}).Where("workspace_id=?", space.ID).Select("kind,count(*) AS count").Group("kind").Scan(&definitions).Error; e != nil {
		s.fail(c, e)
		return
	}
	counts := map[string]int64{}
	for _, v := range definitions {
		counts[v.Kind] = v.Count
	}
	runQuery := tx.Model(&GovernanceRun{}).Where("workspace_id=?", space.ID)
	if !admin(p) {
		runQuery = runQuery.Where("actor_id=?", p.UserID)
	}
	states := []struct {
		State string `json:"state"`
		Count int64  `json:"count"`
	}{}
	if e = runQuery.Select("state,count(*) AS count").Group("state").Scan(&states).Error; e != nil {
		s.fail(c, e)
		return
	}
	recent := []GovernanceRun{}
	q := tx.Where("workspace_id=?", space.ID)
	if !admin(p) {
		q = q.Where("actor_id=?", p.UserID)
	}
	if e = q.Order("created_at DESC,id").Limit(10).Find(&recent).Error; e != nil {
		s.fail(c, e)
		return
	}
	for i := range recent {
		recent[i].Result = json.RawMessage("null")
	}
	var outputs []struct {
		DatasetID         string    `json:"dataset_id"`
		Name              string    `json:"name"`
		PublicationStatus string    `json:"publication_status"`
		CreatedAt         time.Time `json:"created_at"`
	}
	oq := tx.Table("governance_outputs o").Joins("JOIN governance_runs r ON r.id=o.run_id JOIN business_records b ON b.id=o.dataset_id AND b.kind='dataset'").Where("r.workspace_id=?", space.ID)
	if !admin(p) {
		oq = oq.Where("r.actor_id=?", p.UserID)
	}
	if e = oq.Select("o.dataset_id,b.body->'definition'->>'name' AS name,b.state AS publication_status,o.created_at").Order("o.created_at DESC,o.dataset_id").Limit(10).Scan(&outputs).Error; e != nil {
		s.fail(c, e)
		return
	}
	if outputs == nil {
		outputs = []struct {
			DatasetID         string    `json:"dataset_id"`
			Name              string    `json:"name"`
			PublicationStatus string    `json:"publication_status"`
			CreatedAt         time.Time `json:"created_at"`
		}{}
	}
	// Quality counters are execution evidence, not a disclosure of records or a fresh authorization.
	var quality struct {
		Checked int64 `json:"checked"`
		Passed  int64 `json:"passed"`
	}
	qq := tx.Table("governance_run_events ev").Joins("JOIN governance_runs r ON r.id=ev.run_id").Where("r.workspace_id=? AND ev.code='QUALITY_CHECKED'", space.ID)
	if !admin(p) {
		qq = qq.Where("r.actor_id=?", p.UserID)
	}
	if e = qq.Select("COALESCE(sum(ev.input_count),0) AS checked,COALESCE(sum(ev.output_count),0) AS passed").Scan(&quality).Error; e != nil {
		s.fail(c, e)
		return
	}
	var rate *float64
	if quality.Checked > 0 {
		v := float64(quality.Passed) / float64(quality.Checked)
		rate = &v
	}
	c.JSON(200, gin.H{"workspace": space, "source_count": sources, "model_count": counts["model"], "dataflow_count": counts["dataflow"], "workflow_count": counts["workflow"], "run_states": states, "recent_runs": recent, "recent_results": outputs, "quality_summary": gin.H{"checked": quality.Checked, "passed": quality.Passed, "pass_rate": rate, "scope": "检查行次，不代表去重后的数据行数"}, "observed_at": time.Now().UTC()})
}
