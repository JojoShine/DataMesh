package workflow

import (
	"encoding/json"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) checkAssetProduct(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		ExpectedVersion int `json:"expected_version"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	product, revision, e := assetProductRead(tx, p, id, false, true)
	if e != nil {
		return 0, nil, "", e
	}
	if v.ExpectedVersion != product.Version {
		return 0, nil, "", conflict
	}
	members, e := decode[[]AssetProductMember](revision.Members)
	if e != nil {
		return 0, nil, "", e
	}
	definition, e := decode[AssetProductDefinition](revision.Definition)
	if e != nil {
		return 0, nil, "", e
	}
	results := []assetContractResult{}
	overall := []assetContractCheck{}
	now := time.Now().UTC()
	for _, member := range members {
		evidence, err := s.loadAssetEvidence(tx, member.Resource)
		var evidencePointer *assetEvidence
		if err != nil {
			if !assetProductExpectedError(err) {
				return 0, nil, "", err
			}
		} else {
			evidencePointer = &evidence
		}
		checks := assetContractEvaluate(member, definition.Contract, evidencePointer, now)
		results = append(results, assetContractResult{Resource: member.Resource, Name: member.Name, State: assetCheckState(checks), Checks: checks})
		overall = append(overall, checks...)
	}
	checkID, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	record := AssetProductCheck{ID: checkID, ProductID: id, RevisionID: revision.ID, ActorID: p.UserID, State: assetCheckState(overall), Items: encode(results), CheckedAt: now}
	if e = tx.Create(&record).Error; e != nil {
		return 0, nil, "", e
	}
	if record.State == "violated" {
		notice := AssetNotice{UserID: p.UserID, ProductID: id, DedupeKey: "product:" + id + ":" + revision.ID + ":" + fingerprint(record.Items), Category: "product_contract_violated", ResourceName: definition.Name, Message: "主动核验发现产品契约违约，请查看核验结果并联系产品负责人。"}
		if e = createAssetNotice(tx, notice); e != nil {
			return 0, nil, "", e
		}
	}
	return 201, encode(record), id, nil
}
func (s *Server) assetProductChecks(c *gin.Context) {
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	if _, _, e := assetProductRead(tx, p, c.Param("id"), false, false); e != nil {
		s.fail(c, e)
		return
	}
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
	q := tx.Model(&AssetProductCheck{}).Where("product_id=? AND actor_id=?", c.Param("id"), p.UserID)
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []AssetProductCheck{}
	if e = q.Order("checked_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&items).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) assetUsage(c *gin.Context) {
	days, e := intQuery(c, "days", 30, 90)
	if e != nil {
		s.fail(c, e)
		return
	}
	until := time.Now().UTC()
	from := until.Add(-time.Duration(days) * 24 * time.Hour)
	q := s.db.WithContext(c.Request.Context()).Table("audit_records").Where("actor_id=? AND result='success' AND action IN ? AND created_at>=? AND created_at<?", principal(c).UserID, []string{"deliver_raw_object", "deliver_governed_dataset", "deliver_table", "download_file", "invoke_api"}, from, until)
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	type actionCount struct {
		Action string `json:"action"`
		Count  int64  `json:"count"`
	}
	type dayCount struct {
		Date  string `json:"date"`
		Count int64  `json:"count"`
	}
	items := []actionCount{}
	if e = q.Session(&gorm.Session{}).Select("action,count(*) AS count").Group("action").Order("action").Scan(&items).Error; e != nil {
		s.fail(c, e)
		return
	}
	daily := []dayCount{}
	if e = q.Session(&gorm.Session{}).Select("to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD') AS date,count(*) AS count").Group("date").Order("date").Scan(&daily).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"days": days, "from": from, "to": until, "total": total, "items": items, "daily": daily, "cost": nil, "value": nil, "scope": "仅统计当前账号在所选UTC时间窗口内的成功交付审计；查询每页计一次，下载和API按成功请求计次。次数不是数据行数、字节数或使用人数，成本与价值尚未采集。"})
}
