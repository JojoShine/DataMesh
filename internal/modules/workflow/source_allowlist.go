package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SourceTarget struct {
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
}
type sourceAllowedTarget struct {
	ID string `json:"target_id"`
	SourceTarget
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"-"`
}

func (sourceAllowedTarget) TableName() string { return "source_allowed_targets" }

type sourceTargetView struct {
	SourceTarget
	ID        string     `json:"target_id"`
	Origin    string     `json:"origin"`
	CreatedAt *time.Time `json:"created_at"`
}

func staticSourceTargets() []sourceTargetView {
	var environment []SourceTarget
	// Preserve the existing fail-closed behavior for malformed deployment JSON.
	if json.Unmarshal([]byte(os.Getenv("SOURCE_ALLOWED_TARGETS")), &environment) != nil {
		environment = nil
	}
	items := []sourceTargetView{}
	appendTarget := func(v SourceTarget, origin string) {
		if _, ok := sourceType(v.Kind); !ok {
			return
		}
		for _, existing := range items {
			if existing.SourceTarget == v {
				return
			}
		}
		hash := sha256.Sum256(encode(v))
		items = append(items, sourceTargetView{SourceTarget: v, ID: origin + "-" + hex.EncodeToString(hash[:16]), Origin: origin})
	}
	for _, v := range environment {
		appendTarget(v, "environment")
	}
	for _, v := range []SourceTarget{{"postgres", "postgres-source", 5432, "sample"}, {"mysql", "mysql-source", 3306, "sample"}, {"oracle", "oracle-source", 1521, "FREE"}} {
		appendTarget(v, "builtin")
	}
	return items
}
func (rt *Runtime) allowSource(ctx context.Context, metadata *gorm.DB, v SourceInput) error {
	if !validSourceTarget(SourceTarget{Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database}) {
		return invalid
	}
	if endpointAllowed(v) {
		return nil
	}
	var count int64
	if err := metadata.WithContext(ctx).Model(&sourceAllowedTarget{}).Where("kind=? AND host=? AND port=? AND database=? AND revoked_at IS NULL", v.Kind, v.Host, v.Port, v.Database).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return problem(403, "SOURCE_TARGET_DENIED", "目标不在可信数据源白名单中，请联系管理员在数据源管理的「可信数据源」中配置")
	}
	return nil
}

var sourceHostLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validSourceTarget(v SourceTarget) bool {
	_, known := sourceType(v.Kind)
	if !validReaderDatabase(v.Kind, v.Database) {
		return false
	}
	if !known || v.Port < 1 || v.Port > 65535 || !valid(v.Database, 128) || strings.TrimSpace(v.Database) != v.Database || strings.ContainsAny(v.Database, "*\x00\r\n\t") || len(v.Host) > 253 {
		return false
	}
	if net.ParseIP(v.Host) != nil {
		return true
	}
	host := strings.TrimSuffix(v.Host, ".")
	for _, part := range strings.Split(host, ".") {
		if !sourceHostLabel.MatchString(part) {
			return false
		}
	}
	return true
}
func (s *Server) registerSourceAllowlist(api, write *gin.RouterGroup) {
	api.GET("/source-allowed-targets", s.require("exchange", "admin"), func(c *gin.Context) {
		var rows []sourceAllowedTarget
		if err := s.db.WithContext(c.Request.Context()).Where("revoked_at IS NULL").Order("created_at DESC,id").Find(&rows).Error; err != nil {
			s.fail(c, err)
			return
		}
		items := []sourceTargetView{}
		for _, r := range rows {
			items = append(items, sourceTargetView{SourceTarget: r.SourceTarget, ID: r.ID, Origin: "platform", CreatedAt: &r.CreatedAt})
		}
		items = append(items, staticSourceTargets()...)
		c.JSON(200, gin.H{"items": items, "total": len(items)})
	})
	write.POST("/source-allowed-targets", s.require("exchange", "admin"), s.mutate("add_source_allowed_target", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, err := bind[SourceTarget](b)
		if err != nil || !validSourceTarget(v) {
			return 0, nil, "", invalid
		}
		// Serialize capacity checks, including administrators in different server processes.
		if err = tx.Exec("SELECT pg_advisory_xact_lock(619483209)").Error; err != nil {
			return 0, nil, "", err
		}
		if endpointAllowed(SourceInput{Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database}) {
			return 0, nil, "", problem(409, "SOURCE_TARGET_EXISTS", "该目标已在环境配置或内置白名单中")
		}
		var count int64
		if err = tx.Model(&sourceAllowedTarget{}).Where("revoked_at IS NULL").Count(&count).Error; err != nil {
			return 0, nil, "", err
		}
		if count >= 1000 {
			return 0, nil, "", problem(409, "SOURCE_TARGET_LIMIT", "平台白名单最多支持1000条，请先清理不再使用的条目")
		}
		id, err := newID()
		if err != nil {
			return 0, nil, "", err
		}
		row := sourceAllowedTarget{ID: id, SourceTarget: v, CreatedBy: p.UserID}
		if err = tx.Create(&row).Error; err != nil {
			return 0, nil, "", err
		}
		return 201, encode(sourceTargetView{SourceTarget: v, ID: id, Origin: "platform", CreatedAt: &row.CreatedAt}), id, nil
	}))
	write.POST("/source-allowed-targets/:id/remove", s.require("exchange", "admin"), s.mutatePath("remove_source_allowed_target", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, err := bind[struct{}](b); err != nil {
			return 0, nil, id, err
		}
		if strings.HasPrefix(id, "environment-") || strings.HasPrefix(id, "builtin-") {
			return 0, nil, id, problem(409, "SOURCE_TARGET_READ_ONLY", "环境配置和内置白名单为只读，请在部署配置中维护")
		}
		result := tx.Model(&sourceAllowedTarget{}).Where("id=? AND revoked_at IS NULL", id).Update("revoked_at", time.Now().UTC())
		if result.Error != nil {
			return 0, nil, id, result.Error
		}
		if result.RowsAffected == 0 {
			return 0, nil, id, gorm.ErrRecordNotFound
		}
		return 200, encode(gin.H{"target_id": id, "removed": true}), id, nil
	}))
}
