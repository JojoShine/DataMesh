package workflow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type serviceInput struct {
	DirectoryID     string                 `json:"directory_id"`
	ExpectedVersion int                    `json:"expected_directory_version"`
	Name            string                 `json:"name"`
	DatasetID       string                 `json:"dataset_id"`
	Mode            string                 `json:"mode"`
	SQL             string                 `json:"sql"`
	MaxPageSize     int                    `json:"max_page_size"`
	Parameters      map[string]interface{} `json:"parameters"`
}
type DataServiceKey struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	ServiceID    string    `json:"service_id"`
	UserID       string    `json:"-"`
	DepartmentID string    `json:"-"`
	GrantID      string    `json:"-"`
	SecretHash   string    `json:"-"`
	ExpiresAt    time.Time `json:"expires_at"`
	Revoked      bool      `json:"revoked"`
	CreatedAt    time.Time `json:"created_at"`
}

func (DataServiceKey) TableName() string { return "data_service_keys" }
func serviceBody[T any](c *gin.Context) (T, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		var out T
		return out, invalid
	}
	return bind[T](raw)
}
func (s *Server) registerDataServices(api, write *gin.RouterGroup) {
	api.GET("/data-services", s.require("exchange"), func(c *gin.Context) {
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil || page < 1 {
			s.fail(c, invalid)
			return
		}
		size, e := intQuery(c, "page_size", 20, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		q := tx.Model(&Record{}).Where("kind='api_resource' AND body->'config'->'data_service' IS NOT NULL")
		if !admin(p) {
			q = q.Where("owner_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e = q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		out := []gin.H{}
		for _, r := range rows {
			v, e := decode[ManagedResource](r.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			cfg, e := decode[APIConfig](v.Config)
			if e != nil {
				s.fail(c, e)
				return
			}
			out = append(out, gin.H{"id": r.ID, "name": v.Name, "directory_id": r.ParentID, "query": cfg.DataService, "parameters": cfg.Parameters, "fields": v.Fields, "created_at": r.CreatedAt})
		}
		c.JSON(200, gin.H{"items": out, "total": total, "page": page, "page_size": size})
	})
	api.GET("/data-services/sources", s.require("exchange"), func(c *gin.Context) {
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil || page < 1 {
			s.fail(c, invalid)
			return
		}
		size, e := intQuery(c, "page_size", 20, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		q := tx.Where("kind='dataset' AND state='published' AND body->'storage'->>'engine'='doris'")
		if !admin(p) {
			q = q.Where("department_id=?", p.DepartmentID)
		}
		var rows []Record
		if e = q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size + 1).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		more := len(rows) > size
		if more {
			rows = rows[:size]
		}
		out := []gin.H{}
		for _, r := range rows {
			if e = requireResourceCapability(tx, p, Resource{Type: "governed_dataset", ID: r.ID}, "manage_resource"); e != nil {
				var permission *Problem
				if errors.As(e, &permission) && permission.Status == 403 {
					continue
				}
				s.fail(c, e)
				return
			}
			d, e := decode[DatasetData](r.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			out = append(out, gin.H{"id": r.ID, "name": d.Definition.Name, "fields": d.Definition.Fields, "created_at": r.CreatedAt})
		}
		c.JSON(200, gin.H{"items": out, "has_more": more, "page": page, "page_size": size})
	})
	for _, route := range []string{"/data-services/validate", "/data-services"} {
		create := route == "/data-services"
		write.POST(route, s.require("exchange"), func(c *gin.Context) {
			input, e := serviceBody[serviceInput](c)
			if e != nil {
				s.fail(c, e)
				return
			}
			if !valid(input.Name, 200) || input.MaxPageSize < 1 || input.MaxPageSize > 1000 || !slices.Contains([]string{"snapshot", "latest"}, input.Mode) {
				s.fail(c, invalid)
				return
			}
			p := principal(c)
			tx := s.db.WithContext(c.Request.Context())
			source, e := get(tx, "dataset", input.DatasetID, false)
			if e != nil {
				s.fail(c, e)
				return
			}
			data, e := decode[DatasetData](source.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			cfg := &DataServiceQuery{DatasetID: source.ID, Mode: input.Mode, PublisherID: p.UserID, SQL: strings.TrimSpace(input.SQL), MaxPageSize: input.MaxPageSize, SourceFields: data.Definition.Fields}
			parsed, params, e := compileServiceSelect(cfg.SQL, cfg.SourceFields)
			if e != nil {
				s.fail(c, e)
				return
			}
			fields := []Field{}
			for _, name := range parsed.fields {
				for _, f := range cfg.SourceFields {
					if f.Name == name {
						fields = append(fields, f)
					}
				}
			}
			if !allowAPICall(p.UserID + ":service-validation") {
				s.fail(c, problem(429, "API_RATE_LIMIT", "校验频率超过限制"))
				return
			}
			sourceRef := Resource{Type: "governed_dataset", ID: source.ID, Version: source.Version}
			if _, e = s.securityPreviewCheck(tx, p, sourceRef, "invoke"); e != nil {
				s.fail(c, e)
				return
			}
			rows, actual, _, e := s.queryDataService(c.Request.Context(), cfg, fields, input.Parameters, "", 0, min(20, cfg.MaxPageSize))
			if e != nil {
				s.fail(c, e)
				return
			}
			if actual != source.ID {
				current, err := get(tx, "dataset", actual, false)
				if err != nil {
					s.fail(c, err)
					return
				}
				sourceRef.ID = actual
				sourceRef.Version = current.Version
			}
			maskRows(rows, fields, p)
			decision, e := s.securityPreviewCheck(tx, p, sourceRef, "invoke")
			if e != nil {
				s.fail(c, e)
				return
			}
			securityMaskRows(rows, decision.MaskFields)
			if !create {
				if e = s.audit(tx, p, "validate_data_service", source.ID, "success", c.GetString("trace_id")); e != nil {
					s.fail(c, e)
					return
				}
				c.JSON(200, gin.H{"records": rows, "fields": fields, "parameters": params, "dataset_id": actual})
				return
			}
			var result Record
			e = tx.Transaction(func(t *gorm.DB) error {
				if _, e := get(t, "directory", input.DirectoryID, true); e != nil {
					return e
				}
				dir, def, _, e := directoryForResource(t, p, input.DirectoryID)
				if e != nil {
					return e
				}
				if dir.Version != input.ExpectedVersion {
					return conflict
				}
				expected := []Field{}
				if def.Format == "api" && def.DataLayer == "governed_dataset" && def.SharingMethod == "invoke" {
					expected = directoryFields(def)
					if len(expected) != len(fields) {
						return problem(400, "SERVICE_FIELDS_MISMATCH", "目录字段必须与查询输出一致")
					}
				} else if def.Format == "database" && def.SharingMethod == "query" {
					snapshot, shared, mapping, err := serviceCatalogScope(t, dir.ID, cfg)
					if err != nil {
						return err
					}
					cfg.SourceSnapshot = &snapshot
					cfg.CatalogFields = shared
					cfg.CatalogMapping = mapping
					for _, field := range fields {
						found := false
						for _, allowed := range shared {
							if allowed.Name == field.Name {
								expected = append(expected, allowed)
								found = true
								break
							}
						}
						if !found {
							return problem(403, "SERVICE_FIELD_NOT_SHARED", "接口只能使用原目录已经审核共享的字段")
						}
					}
				} else {
					return problem(409, "SERVICE_DIRECTORY_INVALID", "请选择来源成果已启用的业务目录")
				}
				for _, f := range fields {
					match := false
					for _, d := range expected {
						if f.Name == d.Name && f.LogicalType == d.LogicalType && (!f.Sensitive || d.Sensitive) && (!f.Nullable || d.Nullable) && fingerprint(encode(f.Precision)) == fingerprint(encode(d.Precision)) && fingerprint(encode(f.Scale)) == fingerprint(encode(d.Scale)) {
							match = true
						}
					}
					if !match {
						return problem(400, "SERVICE_FIELDS_MISMATCH", "目录字段类型、精度、可空性及敏感级别必须覆盖查询输出")
					}
				}
				if _, _, e = s.serviceSource(c.Request.Context(), t, cfg, actual); e != nil {
					return e
				}
				checked := slices.Clone(cfg.SourceFields)
				if cfg.SourceSnapshot != nil {
					checked = slices.Clone(cfg.CatalogFields)
				}
				for i := range checked {
					for _, f := range expected {
						if f.Name == checked[i].Name && f.Sensitive {
							checked[i].Sensitive = true
						}
					}
				}
				if _, _, e = compileServiceSelect(cfg.SQL, checked); e != nil {
					return e
				}
				apiCfg := APIConfig{DataService: cfg, Method: "POST", QueryOnly: true, AuthType: "none", Parameters: params, TestParameters: input.Parameters}
				result, e = newRecord("api_resource", dir.DepartmentID, p.UserID, dir.ID, "ready", ManagedResource{Name: input.Name, DataLayer: "governed_dataset", Fields: expected, Adapter: "api", Config: encode(apiCfg)})
				if e != nil {
					return e
				}
				if e = t.Create(&result).Error; e != nil {
					return e
				}
				return s.audit(t, p, "create_data_service", result.ID, "success", c.GetString("trace_id"))
			})
			if e != nil {
				s.fail(c, e)
				return
			}
			c.JSON(201, gin.H{"resource": Resource{Type: "api_resource", ID: result.ID, Version: 1}, "directory_id": result.ParentID})
		})
	}
	api.GET("/data-services/:id/keys", s.requireDataConsumer, func(c *gin.Context) {
		keys := []DataServiceKey{}
		e := s.db.WithContext(c.Request.Context()).Where("service_id=? AND user_id=?", c.Param("id"), principal(c).UserID).Order("created_at DESC").Find(&keys).Error
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": keys})
	})
	write.POST("/data-services/:id/keys", s.requireDataConsumer, func(c *gin.Context) {
		in, e := serviceBody[struct {
			Days int `json:"days"`
		}](c)
		if e != nil || in.Days < 1 || in.Days > 90 {
			s.fail(c, invalid)
			return
		}
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		key := DataServiceKey{ServiceID: c.Param("id"), UserID: p.UserID, DepartmentID: p.DepartmentID, ExpiresAt: time.Now().UTC().Add(time.Duration(in.Days) * 24 * time.Hour)}
		key.ID, e = newID()
		if e != nil {
			s.fail(c, e)
			return
		}
		secret := make([]byte, 32)
		if _, e = rand.Read(secret); e != nil {
			s.fail(c, e)
			return
		}
		token := key.ID + "." + hex.EncodeToString(secret)
		sum := sha256.Sum256([]byte(token))
		key.SecretHash = hex.EncodeToString(sum[:])
		e = tx.Transaction(func(t *gorm.DB) error {
			ref := Resource{Type: "api_resource", ID: key.ServiceID, Version: 1}
			grantRecord, grant, _, _, e := s.authorizeDelivery(t, p, ref, DeliveryInvoke, nil)
			if e != nil {
				return e
			}
			_, r, e := managedResource(t, ref, false)
			if e != nil {
				return e
			}
			cfg, e := decode[APIConfig](r.Config)
			if e != nil {
				return e
			}
			if cfg.DataService == nil {
				return invalid
			}
			key.GrantID = grantRecord.ID
			if !admin(p) && key.ExpiresAt.After(grant.Scope.ExpiresAt) {
				key.ExpiresAt = grant.Scope.ExpiresAt
			}
			if e = t.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "service-key:"+p.UserID+":"+key.ServiceID).Error; e != nil {
				return e
			}
			var count int64
			if e = t.Model(&DataServiceKey{}).Where("user_id=? AND service_id=? AND NOT revoked AND expires_at>now()", p.UserID, key.ServiceID).Count(&count).Error; e != nil {
				return e
			}
			if count >= 10 {
				return problem(409, "SERVICE_KEY_LIMIT", "每个接口最多保留 10 个有效凭据，请先撤销不用的凭据")
			}
			if e = t.Create(&key).Error; e != nil {
				return e
			}
			return s.audit(t, p, "create_service_key", key.ID, "success", c.GetString("trace_id"))
		})
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(201, gin.H{"key": key, "token": token})
	})
	write.POST("/data-services/:id/keys/:key/revoke", s.requireDataConsumer, func(c *gin.Context) {
		p := principal(c)
		e := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var key DataServiceKey
			if e := tx.Where("id=? AND service_id=? AND user_id=?", c.Param("key"), c.Param("id"), p.UserID).Take(&key).Error; e != nil {
				return e
			}
			if e := tx.Model(&key).Update("revoked", true).Error; e != nil {
				return e
			}
			return s.audit(tx, p, "revoke_service_key", key.ID, "success", c.GetString("trace_id"))
		})
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"revoked": true})
	})
}
func (s *Server) authenticateService(c *gin.Context) {
	raw := c.GetHeader("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") || len(raw) != 104 {
		s.fail(c, problem(401, "SERVICE_CREDENTIAL_REQUIRED", "请提供有效的接口调用凭据"))
		return
	}
	token := strings.TrimPrefix(raw, "Bearer ")
	sum := sha256.Sum256([]byte(token))
	var key DataServiceKey
	e := s.db.WithContext(c.Request.Context()).Where("secret_hash=? AND service_id=? AND NOT revoked AND expires_at>now()", hex.EncodeToString(sum[:]), c.Param("id")).Take(&key).Error
	if e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			e = problem(401, "SERVICE_CREDENTIAL_INVALID", "调用凭据无效、过期或已撤销")
		}
		s.fail(c, e)
		return
	}
	p, e := s.etlPrincipal(c.Request.Context(), key.UserID)
	if e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			e = denied
		}
		s.fail(c, e)
		return
	}
	if p.DepartmentID != key.DepartmentID || (!platform(p, "exchange") && !platform(p, "assets")) {
		s.fail(c, denied)
		return
	}
	c.Set("principal", p)
	c.Set("service_key_id", key.ID)
	c.Set("service_key_grant", key.GrantID)
	c.Next()
}
