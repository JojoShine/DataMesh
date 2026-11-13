package workflow

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type unitInput struct {
	ParentID        *string `json:"parent_id"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	Code            string  `json:"code"`
	Active          bool    `json:"active"`
	ExpectedVersion int     `json:"expected_version"`
	Reason          string  `json:"reason"`
}

func (s *Server) registerOrganization(api, write *gin.RouterGroup) {
	api.GET("/organization-units", s.require("security", "admin"), func(c *gin.Context) {
		rows := []identity.OrganizationUnit{}
		if err := s.db.WithContext(c.Request.Context()).Order("kind DESC,name,id").Find(&rows).Error; err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": rows})
	})
	write.POST("/organization-units", s.require("security", "admin"), s.mutate("create_organization_unit", s.createUnit))
	write.POST("/organization-units/:id/revisions", s.require("security", "admin"), s.mutatePath("update_organization_unit", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		return s.changeUnit(tx, p, id, b)
	}))
	write.POST("/accounts/:id/revisions", s.require("security", "admin"), s.mutatePath("update_account", s.updateAccount))
	write.POST("/accounts/:id/enable", s.require("security", "admin"), s.mutatePath("enable_account", s.enableAccount))
	api.GET("/directory-provider", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		dept := p.DepartmentID
		if id := c.Query("directory_id"); id != "" {
			r, err := get(tx, "directory", id, false)
			if err != nil {
				s.fail(c, err)
				return
			}
			if err = requireResourceCapability(tx, p, Resource{Type: "directory", ID: id}, "manage_resource"); err != nil {
				s.fail(c, err)
				return
			}
			dept = r.DepartmentID
		}
		value, err := identity.ReadAffiliation(tx, dept)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, value)
	})
}

func (s *Server) createUnit(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	return s.changeUnit(tx, p, "", b)
}

func (s *Server) changeUnit(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, err := bind[unitInput](b)
	if err != nil {
		return 0, nil, id, err
	}
	v.Name = strings.TrimSpace(v.Name)
	v.Code = strings.TrimSpace(v.Code)
	if !valid(v.Name, 200) || !valid(v.Reason, 1000) || (v.Code != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(v.Code)) {
		return 0, nil, id, invalid
	}
	if err = tx.Exec("SELECT pg_advisory_xact_lock(39001)").Error; err != nil {
		return 0, nil, id, err
	}
	row := identity.OrganizationUnit{Kind: v.Kind, ParentID: v.ParentID, Version: 1}
	before := json.RawMessage(`null`)
	status := 201
	if id != "" {
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			return 0, nil, id, err
		}
		before = encode(row)
		if row.Version != v.ExpectedVersion {
			return 0, nil, id, conflict
		}
		if row.Kind != v.Kind || (row.ParentID == nil) != (v.ParentID == nil) || (row.ParentID != nil && *row.ParentID != *v.ParentID) {
			return 0, nil, id, problem(409, "UNIT_PARENT_IMMUTABLE", "所属层级不可直接修改，请创建新部门并办理人员调岗")
		}
		row.Version++
		status = 200
	} else {
		row.ID, err = newID()
		if err != nil {
			return 0, nil, id, err
		}
		id = row.ID
	}
	if !slices.Contains([]string{"organization", "department"}, row.Kind) || (row.Kind == "organization" && row.ParentID != nil) || (row.Kind == "department" && (row.ParentID == nil || *row.ParentID == "")) {
		return 0, nil, id, invalid
	}
	if row.ParentID != nil {
		affiliation, e := identity.ReadAffiliation(tx, *row.ParentID)
		if e != nil {
			return 0, nil, id, e
		}
		if affiliation.Depth >= 64 {
			return 0, nil, id, problem(409, "UNIT_DEPTH_LIMIT", "部门层级超过允许范围")
		}
		if !affiliation.Active {
			return 0, nil, id, problem(409, "UNIT_INACTIVE", "上级组织或部门已停用")
		}
	}
	if !v.Active {
		var people, children int64
		if err = tx.Model(&identity.User{}).Where("department_id=? AND active=true", id).Count(&people).Error; err != nil {
			return 0, nil, id, err
		}
		if err = tx.Model(&identity.OrganizationUnit{}).Where("parent_id=? AND active=true", id).Count(&children).Error; err != nil {
			return 0, nil, id, err
		}
		if people+children > 0 {
			return 0, nil, id, problem(409, "UNIT_IN_USE", "请先停用或调出在用人员，并停用下级部门")
		}
	}
	row.Name, row.Code, row.Active = v.Name, v.Code, v.Active
	if status == 201 {
		err = tx.Create(&row).Error
	} else {
		err = tx.Save(&row).Error
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		err = problem(409, "UNIT_CODE_EXISTS", "单位或部门代码已被使用")
	}
	if err != nil {
		return 0, nil, id, err
	}
	if err = recordIdentityChange(tx, p, "organization_unit", id, v.Reason, before, encode(row)); err != nil {
		return 0, nil, id, err
	}
	return status, encode(row), id, nil
}

type accountUpdate struct {
	DisplayName     string   `json:"display_name"`
	DepartmentID    string   `json:"department_id"`
	Platforms       []string `json:"platforms"`
	Roles           []string `json:"roles"`
	ExpectedVersion int      `json:"expected_version"`
	Reason          string   `json:"reason"`
}

func (s *Server) updateAccount(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, err := bind[accountUpdate](b)
	if err != nil {
		return 0, nil, id, err
	}
	if len(v.DisplayName) > 200 || !valid(v.DepartmentID, 128) || !valid(v.Reason, 1000) {
		return 0, nil, id, invalid
	}
	for _, values := range [][]string{v.Roles, v.Platforms} {
		seen := map[string]bool{}
		for _, value := range values {
			if seen[value] {
				return 0, nil, id, invalid
			}
			seen[value] = true
		}
	}
	for _, value := range v.Roles {
		if !identity.ValidRole(value) {
			return 0, nil, id, invalid
		}
	}
	for _, value := range v.Platforms {
		if !identity.ValidPlatform(value) {
			return 0, nil, id, invalid
		}
	}
	if id == p.UserID && (!slices.Contains(v.Roles, "admin") || v.DepartmentID != p.DepartmentID) {
		return 0, nil, id, problem(409, "SELF_ACCESS_CHANGE_DENIED", "不能在当前会话取消自身管理员角色或调整自身部门")
	}
	if err = tx.Exec("SELECT pg_advisory_xact_lock(39001)").Error; err != nil {
		return 0, nil, id, err
	}
	var row identity.User
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
		return 0, nil, id, err
	}
	if row.Version != v.ExpectedVersion {
		return 0, nil, id, conflict
	}
	if !slices.Contains(v.Roles, "admin") {
		if err = retainAdministrator(tx, id); err != nil {
			return 0, nil, id, err
		}
	}
	unit, err := identity.ReadAffiliation(tx, v.DepartmentID)
	if err != nil {
		return 0, nil, id, err
	}
	if !unit.Active {
		return 0, nil, id, problem(409, "UNIT_INACTIVE", "请选择在用的组织或部门")
	}
	var oldRoles []identity.UserRole
	var oldPlatforms []identity.PlatformAccess
	if err = tx.Where("user_id=?", id).Find(&oldRoles).Error; err != nil {
		return 0, nil, id, err
	}
	if err = tx.Where("user_id=?", id).Find(&oldPlatforms).Error; err != nil {
		return 0, nil, id, err
	}
	before := encode(gin.H{"user": row, "roles": oldRoles, "platforms": oldPlatforms})
	if row.DepartmentID != v.DepartmentID {
		// Department-scoped powers must be explicitly assigned again after a transfer.
		if err = tx.Model(&Record{}).Where("kind IN ('assignment','processing_authorization') AND state='active' AND body->>'user_id'=?", id).Updates(map[string]interface{}{"state": "revoked", "version": gorm.Expr("version+1"), "updated_at": gorm.Expr("now()")}).Error; err != nil {
			return 0, nil, id, err
		}
	}
	row.DisplayName, row.DepartmentID = strings.TrimSpace(v.DisplayName), v.DepartmentID
	row.Version++
	if err = tx.Save(&row).Error; err != nil {
		return 0, nil, id, err
	}
	if err = tx.Where("user_id=?", id).Delete(&identity.UserRole{}).Error; err != nil {
		return 0, nil, id, err
	}
	if err = tx.Where("user_id=?", id).Delete(&identity.PlatformAccess{}).Error; err != nil {
		return 0, nil, id, err
	}
	for _, role := range v.Roles {
		if err = tx.Create(&identity.UserRole{UserID: id, Role: role}).Error; err != nil {
			return 0, nil, id, err
		}
	}
	for _, entry := range v.Platforms {
		if err = tx.Create(&identity.PlatformAccess{UserID: id, Platform: entry}).Error; err != nil {
			return 0, nil, id, err
		}
	}
	if err = tx.Where("user_id=?", id).Delete(&identity.Session{}).Error; err != nil {
		return 0, nil, id, err
	}
	if err = recordIdentityChange(tx, p, "account", id, v.Reason, before, encode(v)); err != nil {
		return 0, nil, id, err
	}
	return 200, encode(gin.H{"user_id": id, "version": row.Version}), id, nil
}

func (s *Server) enableAccount(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, err := bind[struct {
		ExpectedVersion int    `json:"expected_version"`
		Reason          string `json:"reason"`
	}](b)
	if err != nil {
		return 0, nil, id, err
	}
	if !valid(v.Reason, 1000) {
		return 0, nil, id, invalid
	}
	if err = tx.Exec("SELECT pg_advisory_xact_lock(39001)").Error; err != nil {
		return 0, nil, id, err
	}
	var row identity.User
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
		return 0, nil, id, err
	}
	if row.Active || row.Version != v.ExpectedVersion {
		return 0, nil, id, conflict
	}
	unit, err := identity.ReadAffiliation(tx, row.DepartmentID)
	if err != nil {
		return 0, nil, id, err
	}
	if !unit.Active {
		return 0, nil, id, problem(409, "UNIT_INACTIVE", "请先恢复所属组织和部门")
	}
	if err = tx.Model(&row).Updates(map[string]interface{}{"active": true, "version": row.Version + 1}).Error; err != nil {
		return 0, nil, id, err
	}
	if err = recordIdentityChange(tx, p, "account", id, v.Reason, encode(gin.H{"active": false}), encode(gin.H{"active": true})); err != nil {
		return 0, nil, id, err
	}
	return 200, encode(gin.H{"user_id": id, "active": true, "version": row.Version + 1}), id, nil
}

func retainAdministrator(tx *gorm.DB, id string) error {
	var target int64
	if err := tx.Model(&identity.UserRole{}).Where("user_id=? AND role='admin'", id).Count(&target).Error; err != nil {
		return err
	}
	if target == 0 {
		return nil
	}
	var others int64
	if err := tx.Table("users u").Joins("JOIN user_roles r ON r.user_id=u.id AND r.role='admin'").Where("u.active=true AND u.id<>?", id).Count(&others).Error; err != nil {
		return err
	}
	if others == 0 {
		return problem(409, "LAST_ADMIN_REQUIRED", "必须保留至少一位在用管理员")
	}
	return nil
}

func recordIdentityChange(tx *gorm.DB, p identity.Principal, kind, id, reason string, before, after json.RawMessage) error {
	return tx.Exec("INSERT INTO identity_change_records(actor_id,subject_kind,subject_id,reason,before_value,after_value) VALUES(?,?,?,?,?::jsonb,?::jsonb)", p.UserID, kind, id, reason, string(before), string(after)).Error
}
