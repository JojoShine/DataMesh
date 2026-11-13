package workflow

import (
	"context"
	"encoding/json"
	"mime"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) etlReportAccess(ctx context.Context, tx *gorm.DB, p identity.Principal, runID string, lock bool) (Record, ETLRun, error) {
	record, e := etlOwner(tx, p, "etl_run", runID, lock)
	if e != nil {
		return record, ETLRun{}, e
	}
	run, e := decode[ETLRun](record.Body)
	if e != nil {
		return record, run, e
	}
	if run.InputSnapshot != nil {
		run.authorization = run.InputSnapshot.Authorization
	}
	recipient, e := s.etlPrincipal(ctx, record.OwnerID)
	if e != nil {
		return record, run, denied
	}
	if e = s.etlPermission(tx, recipient, run.Definition, run.authorization); e != nil {
		return record, run, e
	}
	return record, run, nil
}
func (s *Server) registerETLReconciliation(api, write *gin.RouterGroup) {
	api.GET("/etl-runs/:id/reconciliation", s.require("exchange", "operator"), s.etlReconciliationReport)
	write.POST("/etl-runs/:id/reconciliation", s.require("exchange", "operator"), s.mutatePath("reconcile_etl_delivery", func(tx *gorm.DB, p identity.Principal, id string, body []byte) (int, json.RawMessage, string, error) {
		if _, e := bind[struct{}](body); e != nil {
			return 0, nil, id, e
		}
		record, run, e := s.etlReportAccess(tx.Statement.Context, tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if record.State != "succeeded" {
			return 0, nil, id, problem(409, "DELIVERY_NOT_COMMITTED", "仅确认已提交的运行可执行交付对账；未知结果须先核实事务")
		}
		var input etlDeliveryInput
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id=?", id).Take(&input).Error; e != nil {
			return 0, nil, id, problem(409, "DELIVERY_INDEX_UNAVAILABLE", "历史运行没有可用的交付核对索引")
		}
		if input.State != "ready" || !input.ExpiresAt.After(time.Now()) {
			return 0, nil, id, problem(409, "DELIVERY_INDEX_EXPIRED", "交付核对索引已清理或过期，不能重新核对")
		}
		var active int64
		if e = tx.Model(&ETLReconciliation{}).Where("run_id=? AND state IN ('pending','checking')", id).Count(&active).Error; e != nil {
			return 0, nil, id, e
		}
		if active > 0 {
			return 0, nil, id, problem(409, "RECONCILIATION_PENDING", "已有交付对账正在进行")
		}
		var version int
		if e = tx.Model(&ETLReconciliation{}).Where("run_id=?", id).Select("COALESCE(MAX(version),0)").Scan(&version).Error; e != nil {
			return 0, nil, id, e
		}
		reportID, e := newID()
		if e != nil {
			return 0, nil, id, e
		}
		report := ETLReconciliation{ID: reportID, RunID: id, Version: version + 1, State: "pending", InputScope: json.RawMessage("null"), TaskVersion: run.TaskVersion, ReadRows: int64(run.Read), ProcessedRows: int64(run.Read), KeyCount: input.KeyCount}
		if run.InputSnapshot != nil {
			report.InputSnapshotID = run.InputSnapshot.SnapshotID
			report.InputScope = encode(run.InputSnapshot)
		}
		if e = tx.Create(&report).Error; e != nil {
			return 0, nil, id, e
		}
		return 202, encode(gin.H{"run_id": id, "reconciliation_id": reportID, "version": report.Version}), id, nil
	}))
}
func (s *Server) etlReconciliationReport(c *gin.Context) {
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
	var response json.RawMessage
	var filename string
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		record, _, err := s.etlReportAccess(c.Request.Context(), tx, principal(c), c.Param("id"), false)
		if err != nil {
			return err
		}
		var latest []ETLReconciliation
		if err = tx.Where("run_id=?", record.ID).Order("version DESC").Limit(1).Find(&latest).Error; err != nil {
			return err
		}
		if c.Query("download") == "1" {
			if len(latest) == 0 {
				return problem(404, "DELIVERY_NOT_RECONCILED", "历史运行尚未执行对账")
			}
			response = encode(latest[0])
			filename = mime.FormatMediaType("attachment", map[string]string{"filename": "delivery-reconciliation-" + record.ID + ".json"})
			return s.audit(tx, principal(c), "export_delivery_reconciliation", record.ID, "success", c.GetString("trace_id"))
		}
		var history []ETLReconciliation
		var total int64
		if err = tx.Model(&ETLReconciliation{}).Where("run_id=?", record.ID).Count(&total).Error; err != nil {
			return err
		}
		if err = tx.Where("run_id=?", record.ID).Order("version DESC").Offset((page - 1) * size).Limit(size).Find(&history).Error; err != nil {
			return err
		}
		var current *ETLReconciliation
		if len(latest) > 0 {
			current = &latest[0]
		}
		var inputs []etlDeliveryInput
		if err = tx.Select("run_id", "state", "expires_at", "receipt_version").Where("run_id=?", record.ID).Limit(1).Find(&inputs).Error; err != nil {
			return err
		}
		canRetry := record.State == "succeeded" && len(inputs) > 0 && inputs[0].State == "ready" && inputs[0].ExpiresAt.After(time.Now()) && (current == nil || (current.State != "pending" && current.State != "checking"))
		response = encode(gin.H{"run_id": record.ID, "latest": current, "history": history, "total": total, "page": page, "page_size": size, "can_retry": canRetry, "recipient_id": record.OwnerID, "execution_state": record.State})
		return nil
	})
	if e != nil {
		s.fail(c, e)
		return
	}
	if filename != "" {
		c.Header("Content-Disposition", filename)
	}
	c.Data(200, "application/json", response)
}
