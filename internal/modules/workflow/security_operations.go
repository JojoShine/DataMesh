package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"strings"
	"time"
)

func (s *Server) securityOverview(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	counts := map[string]int64{}
	for _, kind := range []string{"classification", "policy", "watermark", "recognition", "risk_rule"} {
		var n int64
		if e := tx.Model(&SecurityControl{}).Where("kind=? AND published_version>0", kind).Count(&n).Error; e != nil {
			s.fail(c, e)
			return
		}
		counts[kind] = n
	}
	for key, query := range map[string]string{"pending": "SELECT count(*) FROM security_control_versions WHERE state='submitted'", "open_incidents": "SELECT count(*) FROM security_incidents WHERE state<>'resolved'", "frozen": "SELECT count(*) FROM security_incidents WHERE state='frozen'", "proofs": "SELECT count(*) FROM security_delivery_proofs", "expired_grants": "SELECT count(*) FROM business_records WHERE kind='grant' AND state='active' AND (body->'scope'->>'expires_at')::timestamptz<=now()", "denied_24h": "SELECT count(*) FROM audit_records WHERE result='denied' AND created_at>=now()-interval '24 hours'"} {
		var n int64
		if e := tx.Raw(query).Scan(&n).Error; e != nil {
			s.fail(c, e)
			return
		}
		counts[key] = n
	}
	id, _, e := securitySigningKey("")
	c.JSON(200, gin.H{"counts": counts, "checked_at": time.Now().UTC(), "scope": "获准安全管理/审计人员可见的全平台配置与业务审计；不代表源数据库的全部外部访问", "signing_available": e == nil, "signing_key_id": id, "capabilities": gin.H{"excel_content_mark": true, "pdf_content_mark": true, "invisible_watermark": true, "row_filters": true}})
}
func (s *Server) securityScans(c *gin.Context) {
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
	q := s.db.WithContext(c.Request.Context()).Model(&SecurityScan{})
	var n int64
	if e = q.Count(&n).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []SecurityScan{}
	if e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": n, "page": page, "page_size": size})
}
func (s *Server) scanSecurityMetadata(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		Resource Resource `json:"resource"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	_, fields, e := resource(tx, v.Resource, false)
	if e != nil {
		return 0, nil, "", e
	}
	rules, e := securityPublishedControls(tx, []string{"recognition"})
	if e != nil {
		return 0, nil, "", e
	}
	candidates := []gin.H{}
	builtin := regexp.MustCompile(`(?i)(password|passwd|secret|token|身份证|手机号|电话|email|phone|id_card|bank|银行卡)`)
	for _, field := range fields {
		if field.Sensitive || builtin.MatchString(field.Name) {
			candidates = append(candidates, gin.H{"field": field.Name, "level": "sensitive", "rule": "现有敏感标记/元数据名称识别", "state": "candidate"})
		}
		for _, rule := range rules {
			d, err := decode[SecurityDefinition](rule.Definition)
			if err != nil {
				return 0, nil, "", err
			}
			r, err := regexp.Compile(d.Pattern)
			if err != nil {
				return 0, nil, "", err
			}
			if r.MatchString(field.Name) {
				candidates = append(candidates, gin.H{"field": field.Name, "level": d.Level, "rule": d.Name, "state": "candidate"})
			}
		}
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	row := SecurityScan{ID: id, ActorID: p.UserID, Resource: encode(v.Resource), Candidates: encode(candidates), CreatedAt: time.Now().UTC()}
	if e = tx.Create(&row).Error; e != nil {
		return 0, nil, "", e
	}
	return 201, encode(row), id, nil
}
func (s *Server) securityIncidents(c *gin.Context) {
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
	q := s.db.WithContext(c.Request.Context()).Model(&SecurityIncident{})
	if id := c.Query("resource_id"); id != "" {
		q = q.Where("resource_id=?", id)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []SecurityIncident{}
	if e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
func (s *Server) createSecurityIncident(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		Resource Resource `json:"resource"`
		Title    string   `json:"title"`
		Reason   string   `json:"reason"`
	}](b)
	if e != nil || !valid(v.Title, 200) || !valid(v.Reason, 2000) {
		return 0, nil, "", invalid
	}
	if _, _, e = resource(tx, v.Resource, false); e != nil {
		return 0, nil, "", e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	row := SecurityIncident{ID: id, Resource: encode(v.Resource), ResourceID: v.Resource.ID, Title: v.Title, Reason: v.Reason, State: "open", OwnerID: p.UserID, Generation: 1, Evidence: encode(gin.H{"source": "manual", "recorded_at": time.Now().UTC()}), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if e = tx.Create(&row).Error; e != nil {
		return 0, nil, "", e
	}
	return 201, encode(row), id, nil
}
func (s *Server) securityIncidentAction(action string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			ExpectedVersion int    `json:"expected_version"`
			Reason          string `json:"reason"`
		}](b)
		if e != nil || !valid(v.Reason, 2000) {
			return 0, nil, "", invalid
		}
		var row SecurityIncident
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; e != nil {
			return 0, nil, "", e
		}
		if row.Generation != v.ExpectedVersion || row.State == "resolved" {
			return 0, nil, "", conflict
		}
		if action == "freeze" {
			if row.State != "open" {
				return 0, nil, "", conflict
			}
			row.State = "frozen"
			row.FrozenBy = p.UserID
		} else {
			if row.OwnerID == p.UserID || row.FrozenBy == p.UserID {
				return 0, nil, "", denied
			}
			row.State = "resolved"
			row.ReviewedBy = p.UserID
		}
		evidence, err := decode[map[string]json.RawMessage](row.Evidence)
		if err != nil {
			return 0, nil, "", err
		}
		events := []gin.H{}
		if raw, ok := evidence["events"]; ok {
			if err = json.Unmarshal(raw, &events); err != nil {
				return 0, nil, "", err
			}
		}
		events = append(events, gin.H{"action": action, "actor_id": p.UserID, "reason": v.Reason, "at": time.Now().UTC()})
		evidence["events"] = encode(events)
		row.Evidence = encode(evidence)
		row.Generation++
		row.Reason = v.Reason
		row.UpdatedAt = time.Now().UTC()
		if e = tx.Save(&row).Error; e != nil {
			return 0, nil, "", e
		}
		return 200, encode(row), id, nil
	}
}
func (s *Server) securityRiskCheck(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, "", e
	}
	rules, e := securityPublishedControls(tx, []string{"risk_rule"})
	if e != nil {
		return 0, nil, "", e
	}
	created := int64(0)
	for _, rule := range rules {
		d, err := decode[SecurityDefinition](rule.Definition)
		if err != nil {
			return 0, nil, "", err
		}
		type signal struct {
			ActorID  string
			ObjectID string
			Count    int64
		}
		signals := []signal{}
		q := tx.Table("audit_records").Select("actor_id,object_id,count(*) AS count").Where("created_at>=? AND result IN ('denied','failed') AND object_id<>''", time.Now().UTC().Add(-time.Duration(d.WindowMinutes)*time.Minute))
		if d.Resource != nil {
			q = q.Where("object_id=?", d.Resource.ID)
		}
		if err = q.Group("actor_id,object_id").Having("count(*)>=?", d.Threshold).Order("count(*) DESC").Limit(100).Scan(&signals).Error; err != nil {
			return 0, nil, "", err
		}
		for _, signal := range signals {
			if d.Resource == nil {
				continue
			}
			key := rule.ID + ":" + string(encode(rule.Version)) + ":" + signal.ActorID + ":" + signal.ObjectID + ":" + time.Now().UTC().Format("2006-01-02")
			id, err := newID()
			if err != nil {
				return 0, nil, "", err
			}
			row := SecurityIncident{ID: id, Resource: encode(d.Resource), ResourceID: d.Resource.ID, Title: d.Name, Reason: "主动核对发现重复拒绝/失败访问；需要人工核实，不自动认定泄露", State: "open", OwnerID: p.UserID, Generation: 1, DedupeKey: &key, Evidence: encode(gin.H{"rule": rule.ID, "version": rule.Version, "actor_id": signal.ActorID, "count": signal.Count, "window_minutes": d.WindowMinutes}), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedupe_key"}}, DoNothing: true}).Create(&row)
			if result.Error != nil {
				return 0, nil, "", result.Error
			}
			created += result.RowsAffected
		}
	}
	return 200, encode(gin.H{"created": created, "scope": strings.TrimSpace("仅主动核对已发布规则范围内的拒绝和失败审计，不代表实时监测或源数据库完整访问日志")}), "", nil
}

func (s *Server) securityResources(c *gin.Context) {
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
	search := c.Query("search")
	if len(search) > 200 {
		s.fail(c, invalid)
		return
	}
	tx := s.db.WithContext(c.Request.Context())
	q := tx.Model(&Record{}).Where("((kind='object' AND state IN ('available','ready')) OR (kind='dataset' AND state='published') OR (kind IN ('table_resource','api_resource','file_resource') AND state='ready'))")
	if search != "" {
		q = q.Where("COALESCE(body->'definition'->>'name',body->>'name',body->>'qualified_name',id) ILIKE ? OR id=?", "%"+search+"%", search)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []Record{}
	if e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		kind := row.Kind
		if kind == "object" {
			kind = "raw_object"
		}
		if kind == "dataset" {
			kind = "governed_dataset"
		}
		ref := Resource{Type: kind, ID: row.ID, Version: row.Version}
		var data struct {
			Name          string  `json:"name"`
			QualifiedName string  `json:"qualified_name"`
			Fields        []Field `json:"fields"`
			Definition    struct {
				Name   string  `json:"name"`
				Fields []Field `json:"fields"`
			} `json:"definition"`
		}
		if e = json.Unmarshal(row.Body, &data); e != nil {
			s.fail(c, e)
			return
		}
		fields := data.Fields
		name := data.Name
		if row.Kind == "dataset" {
			fields = data.Definition.Fields
			name = data.Definition.Name
		}
		if name == "" {
			name = data.QualifiedName
		}
		if name == "" {
			name = row.ID
		}
		if fields == nil {
			fields = []Field{}
		}
		items = append(items, gin.H{"resource": ref, "name": name, "fields": fields})
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
