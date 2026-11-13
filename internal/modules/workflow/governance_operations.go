package workflow

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerGovernanceOperations(api, write *gin.RouterGroup) {
	s.registerGovernanceQualityIssues(api, write)
	s.registerGovernanceAlerts(api, write)
	s.registerGovernanceLifecycle(api, write)
	api.GET("/governance/impact", s.require("governance", "governor"), s.governanceImpactGraph)
	api.GET("/governance/quality-trends", s.require("governance", "governor"), s.governanceQualityTrend)
	api.GET("/governance/operations", s.require("governance", "governor"), s.governanceOperations)
	api.GET("/governance/dashboard", s.require("governance", "governor"), s.governanceDashboard)
	api.GET("/governance/lineage", s.require("governance", "governor"), s.governanceLineage)
}
func (s *Server) governanceOperations(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	runs := workspaceQuery(tx.Model(&GovernanceRun{}), "workspace_id").Where("workspace_id IS NOT NULL")
	issues := workspaceQuery(tx.Model(&GovernanceQualityIssue{}), "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id)").Where("(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id) IS NOT NULL")
	defs := workspaceQuery(tx.Model(&GovernanceDefinitionRecord{}), "workspace_id")
	if !admin(p) {
		runs = runs.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		issues = issues.Where("department_id=? AND (actor_id=? OR assignee_id=?)", p.DepartmentID, p.UserID, p.UserID)
		defs = defs.Where("department_id=?", p.DepartmentID)
	}
	var counts []struct {
		State string `json:"state"`
		Count int64  `json:"count"`
	}
	if e := runs.Select("state,count(*) AS count").Group("state").Scan(&counts).Error; e != nil {
		s.fail(c, e)
		return
	}
	var definitions []struct {
		Kind  string `json:"kind"`
		Count int64  `json:"count"`
	}
	if e := defs.Select("kind,count(*) AS count").Group("kind").Scan(&definitions).Error; e != nil {
		s.fail(c, e)
		return
	}
	var unresolved, overdue int64
	if e := issues.Where("state NOT IN ('closed','superseded')").Count(&unresolved).Error; e != nil {
		s.fail(c, e)
		return
	}
	if e := issues.Where("state NOT IN ('closed','superseded') AND due_at<CURRENT_TIMESTAMP").Count(&overdue).Error; e != nil {
		s.fail(c, e)
		return
	}
	recent := []GovernanceRun{}
	q := workspaceQuery(tx.Where("kind IN ('profile','dataflow','workflow','dataflow_preview','master')"), "workspace_id").Where("workspace_id IS NOT NULL")
	if !admin(p) {
		q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
	}
	if e := q.Order("created_at DESC,id").Limit(10).Find(&recent).Error; e != nil {
		s.fail(c, e)
		return
	}
	for i := range recent {
		recent[i].Result = json.RawMessage("null")
	}
	c.JSON(200, gin.H{"run_states": counts, "definitions": definitions, "open_issues": unresolved, "overdue_issues": overdue, "recent_runs": recent, "observed_at": time.Now().UTC()})
}
func (s *Server) governanceLineage(c *gin.Context) {
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	resourceID := c.Query("resource_id")
	if resourceID != "" && !valid(resourceID, 128) {
		s.fail(c, invalid)
		return
	}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	q := workspaceQuery(tx.Model(&GovernanceRun{}).Where("workspace_id IS NOT NULL AND kind='dataflow' AND state='succeeded'"), "workspace_id")
	if !admin(p) {
		q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
	}
	if resourceID != "" {
		q = q.Where("EXISTS(SELECT 1 FROM jsonb_each(definition->'inputs') AS i WHERE i.value->'resource'->>'resource_id'=?) OR result->>'dataset_id'=?", resourceID, resourceID)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []GovernanceRun{}
	if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []gin.H{}
	for _, run := range rows {
		def, e := decode[governanceFlowExecution](run.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		actor, e := s.etlPrincipal(c.Request.Context(), run.ActorID)
		if e != nil {
			s.fail(c, denied)
			return
		}
		if e = s.governanceFlowAuthorize(c.Request.Context(), actor, def); e != nil {
			s.fail(c, e)
			return
		}
		report, e := decode[governanceFlowReport](run.Result)
		if e != nil {
			s.fail(c, e)
			return
		}
		datasets := []Record{}
		if e = tx.Where("kind='dataset' AND body->>'governance_run_id'=?", run.ID).Find(&datasets).Error; e != nil {
			s.fail(c, e)
			return
		}
		outputs := []gin.H{}
		for _, d := range datasets {
			outputs = append(outputs, gin.H{"dataset_id": d.ID, "contract_version": d.Version, "publication_status": d.State})
		}
		items = append(items, gin.H{"data_layer": report.DataLayer, "storage": report.Storage, "engine": report.Engine, "run_id": run.ID, "flow": report.Flow, "model": report.Model, "fields": report.Lineage, "nodes": report.Nodes, "outputs": outputs, "created_at": run.CreatedAt})
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
