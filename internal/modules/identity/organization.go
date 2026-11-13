package identity

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrganizationUnit struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	Code     string  `json:"code"`
	Active   bool    `json:"active"`
	Version  int     `json:"version"`
}

type Affiliation struct {
	DepartmentID   string `json:"department_id"`
	DepartmentName string `json:"department_name"`
	OrganizationID string `json:"organization_id"`
	ProviderName   string `json:"provider_name"`
	ProviderCode   string `json:"provider_code"`
	Active         bool   `json:"active"`
	Depth          int    `json:"-"`
}

func EnsureLegacyUnit(tx *gorm.DB, id string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OrganizationUnit{ID: id, Kind: "organization", Name: id, Active: true, Version: 1}).Error
}

func ReadAffiliation(tx *gorm.DB, id string) (Affiliation, error) {
	var out Affiliation
	err := tx.Raw(`WITH RECURSIVE chain AS (
 SELECT id,parent_id,kind,name,code,active,0 AS depth FROM organization_units WHERE id=?
 UNION ALL SELECT u.id,u.parent_id,u.kind,u.name,u.code,u.active,c.depth+1
 FROM organization_units u JOIN chain c ON u.id=c.parent_id WHERE c.depth<64
 ) SELECT ? AS department_id,
 (SELECT name FROM chain WHERE depth=0) AS department_name,
 id AS organization_id,name AS provider_name,code AS provider_code,depth,
 (SELECT bool_and(active) FROM chain) AS active FROM chain WHERE kind='organization'`, id, id).Scan(&out).Error
	if err == nil && out.OrganizationID == "" {
		return out, gorm.ErrRecordNotFound
	}
	return out, err
}
