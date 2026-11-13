package workflow

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DeliveryReceiptInput struct {
	Decision         string `json:"decision"`
	Reason           string `json:"reason"`
	ExpectedVersion  int    `json:"expected_version"`
	ReconciliationID string `json:"reconciliation_id"`
	OnBehalfReason   string `json:"on_behalf_reason,omitempty"`
}
type DeliveryReceipt struct {
	ID               string    `json:"id" gorm:"primaryKey"`
	RunID            string    `json:"run_id"`
	Version          int       `json:"version"`
	ReconciliationID string    `json:"reconciliation_id"`
	Decision         string    `json:"decision"`
	Reason           string    `json:"reason"`
	ActorID          string    `json:"actor_id"`
	RecipientID      string    `json:"recipient_id"`
	OnBehalfReason   string    `json:"on_behalf_reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func (DeliveryReceipt) TableName() string { return "delivery_receipts" }
func validReceiptReason(reason string) bool {
	return strings.TrimSpace(reason) != "" && utf8.RuneCountInString(reason) <= 1000
}
func (s *Server) registerDeliveryReceipts(api, write *gin.RouterGroup) {
	write.POST("/etl-runs/:id/receipts", s.require("exchange", "operator"), s.mutatePath("create_delivery_receipt", s.createDeliveryReceipt))
	api.GET("/etl-runs/:id/receipts", s.require("exchange", "operator"), func(c *gin.Context) {
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
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			record, _, err := s.etlReportAccess(c.Request.Context(), tx, principal(c), c.Param("id"), false)
			if err != nil {
				return err
			}
			var inputs []etlDeliveryInput
			if err = tx.Select("run_id", "receipt_version").Where("run_id=?", record.ID).Limit(1).Find(&inputs).Error; err != nil {
				return err
			}
			version := 1
			if len(inputs) > 0 {
				version = inputs[0].ReceiptVersion
			}
			var items []DeliveryReceipt
			var total int64
			if err = tx.Model(&DeliveryReceipt{}).Where("run_id=?", record.ID).Count(&total).Error; err != nil {
				return err
			}
			if err = tx.Where("run_id=?", record.ID).Order("version DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
				return err
			}
			var reports []ETLReconciliation
			if err = tx.Where("run_id=?", record.ID).Order("version DESC").Limit(1).Find(&reports).Error; err != nil {
				return err
			}
			canReject := record.State == "succeeded" && len(reports) > 0 && len(inputs) > 0
			canAccept := canReject && reports[0].State == "passed"
			var latest []DeliveryReceipt
			if err = tx.Where("run_id=?", record.ID).Order("version DESC").Limit(1).Find(&latest).Error; err != nil {
				return err
			}
			state := "pending"
			if len(latest) > 0 {
				state = latest[0].Decision
			} else if record.State != "succeeded" {
				state = "not_applicable"
			}
			response = encode(gin.H{"run_id": record.ID, "version": version, "state": state, "items": items, "total": total, "page": page, "page_size": size, "recipient_id": record.OwnerID, "can_accept": canAccept, "can_reject": canReject, "delegate": principal(c).UserID != record.OwnerID})
			return nil
		})
		if e != nil {
			s.fail(c, e)
			return
		}
		c.Data(200, "application/json", response)
	})
}
func (s *Server) createDeliveryReceipt(tx *gorm.DB, p identity.Principal, id string, body []byte) (int, json.RawMessage, string, error) {
	input, e := bind[DeliveryReceiptInput](body)
	if e != nil {
		return 0, nil, id, e
	}
	if (input.Decision != "accepted" && input.Decision != "rejected") || !validReceiptReason(input.Reason) || input.ExpectedVersion < 1 || !valid(input.ReconciliationID, 128) {
		return 0, nil, id, invalid
	}
	record, _, e := s.etlReportAccess(tx.Statement.Context, tx, p, id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if record.State != "succeeded" {
		return 0, nil, id, problem(409, "DELIVERY_NOT_COMMITTED", "运行须确认已提交后才能填写验收；未知结果请先核实事务")
	}
	delegate := p.UserID != record.OwnerID
	if delegate && (!admin(p) || !validReceiptReason(input.OnBehalfReason)) {
		return 0, nil, id, problem(400, "RECEIPT_DELEGATE_REASON_REQUIRED", "管理员代办须填写代办依据与原因")
	}
	if !delegate && input.OnBehalfReason != "" {
		return 0, nil, id, invalid
	}
	var delivery etlDeliveryInput
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id=?", id).Take(&delivery).Error; e != nil {
		return 0, nil, id, problem(409, "DELIVERY_NOT_RECONCILED", "历史运行尚未执行交付对账，不能补造验收依据")
	}
	if delivery.ReceiptVersion != input.ExpectedVersion {
		return 0, nil, id, problem(409, "RECEIPT_VERSION_CHANGED", "验收记录已更新，请刷新后再确认")
	}
	var reports []ETLReconciliation
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id=?", id).Order("version DESC").Limit(1).Find(&reports).Error; e != nil {
		return 0, nil, id, e
	}
	if len(reports) == 0 || reports[0].ID != input.ReconciliationID {
		return 0, nil, id, problem(409, "RECONCILIATION_VERSION_CHANGED", "对账报告已更新，请刷新并核对最新报告")
	}
	if input.Decision == "accepted" && reports[0].State != "passed" {
		return 0, nil, id, problem(409, "RECEIPT_RECONCILIATION_REQUIRED", "最新技术对账尚未通过，暂不能确认接收")
	}
	receiptID, e := newID()
	if e != nil {
		return 0, nil, id, e
	}
	receipt := DeliveryReceipt{ID: receiptID, RunID: id, Version: delivery.ReceiptVersion + 1, ReconciliationID: reports[0].ID, Decision: input.Decision, Reason: strings.TrimSpace(input.Reason), ActorID: p.UserID, RecipientID: record.OwnerID, OnBehalfReason: strings.TrimSpace(input.OnBehalfReason)}
	if e = tx.Create(&receipt).Error; e != nil {
		return 0, nil, id, e
	}
	delivery.ReceiptVersion = receipt.Version
	if e = tx.Model(&delivery).Update("receipt_version", receipt.Version).Error; e != nil {
		return 0, nil, id, e
	}
	return 201, encode(receipt), id, nil
}
