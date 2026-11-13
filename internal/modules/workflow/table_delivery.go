package workflow

import (
	"context"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (s *Server) deliverTable(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	tx := s.db.WithContext(ctx)
	p := principal(c)
	version, e := strconv.Atoi(c.Query("contract_version"))
	if e != nil || version < 1 {
		s.fail(c, invalid)
		return
	}
	limit, e := intQuery(c, "limit", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	ref := Resource{Type: "table_resource", ID: c.Param("id"), Version: version}
	requested := []string{}
	for _, v := range c.QueryArray("fields") {
		requested = append(requested, strings.Split(v, ",")...)
	}
	var grant Record
	var schema []Field
	var fields []string
	e = tx.Transaction(func(t *gorm.DB) error {
		var err error
		grant, _, schema, fields, err = s.authorizeDelivery(t, p, ref, DeliveryQuery, requested)
		return err
	})
	if e != nil {
		s.fail(c, e)
		return
	}
	slices.Sort(fields)
	decision, err := s.securityPolicyCheck(tx, p, ref, "query")
	if err != nil {
		s.fail(c, err)
		return
	}
	hash := fingerprint(encode(fields))
	if len(decision.RowFilters) > 0 {
		hash = fingerprint(encode([]interface{}{fields, decision.RowFilters}))
	}
	c.Set("security_row_filter_basis", fingerprint(encode(decision.RowFilters)))
	var after []string
	if token := c.Query("cursor"); token != "" {
		cursor, err := s.parseCursor(token)
		if err != nil {
			s.fail(c, err)
			return
		}
		if cursor.Resource != ref || cursor.Grant != grant.ID || cursor.Fields != hash {
			s.fail(c, conflict)
			return
		}
		after = cursor.Keys
	}
	_, v, e := managedResource(tx, ref, false)
	if e != nil {
		s.fail(c, e)
		return
	}
	rows, last, e := s.readTable(ctx, tx, v, fields, after, limit, true, decision.RowFilters)
	if e != nil {
		s.fail(c, e)
		return
	}
	maskRows(rows, schema, p)
	var next *string
	if len(rows) == limit {
		token, err := s.signCursor(Cursor{Grant: grant.ID, Resource: ref, Fields: hash, Keys: last})
		if err != nil {
			s.fail(c, err)
			return
		}
		next = &token
	}
	e = tx.Transaction(func(t *gorm.DB) error {
		_, _, _, _, err := s.authorizeDelivery(t, p, ref, DeliveryQuery, fields)
		if err != nil {
			return err
		}
		return nil
	})
	if e != nil {
		s.fail(c, e)
		return
	}
	securityMaskRows(rows, decision.MaskFields)
	if err = s.securitySend(c, p, ref, "query", grant.ID, fields, encode(gin.H{"resource": ref, "records": rows, "next_cursor": next}), "application/json"); err != nil && !c.Writer.Written() {
		s.fail(c, err)
	}
}
func (s *Server) registerManagedResources(api *gin.RouterGroup) {
	api.GET("/managed-resources", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		dir := c.Query("directory_id")
		if dir == "" {
			s.fail(c, invalid)
			return
		}
		if _, _, _, e := directoryForResource(tx, p, dir); e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e := tx.Where("kind IN ? AND parent_id=? AND state='ready'", []string{"table_resource", "file_resource", "api_resource"}, dir).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []interface{}{}
		for _, r := range rows {
			v, e := decode[ManagedResource](r.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			item := gin.H{"resource": Resource{Type: r.Kind, ID: r.ID, Version: r.Version}, "name": v.Name, "fields": v.Fields, "adapter": v.Adapter, "data_layer": v.DataLayer}
			if r.Kind == "api_resource" {
				cfg, err := decode[APIConfig](v.Config)
				if err != nil {
					s.fail(c, err)
					return
				}
				if cfg.DataService != nil && cfg.DataService.SourceSnapshot != nil {
					mapping := map[string]string{}
					for key, value := range cfg.DataService.CatalogMapping {
						for _, field := range v.Fields {
							if field.Name == value {
								mapping[key] = value
							}
						}
					}
					item["service_representation"] = true
					item["mapping"] = mapping
				}
			}
			items = append(items, item)
		}
		c.JSON(200, gin.H{"items": items})
	})
}
