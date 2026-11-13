package workflow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Cursor struct {
	Keys     []string `json:"keys,omitempty"`
	Grant    string   `json:"grant"`
	Resource Resource `json:"resource"`
	Fields   string   `json:"fields"`
	Cutoff   string   `json:"cutoff"`
	Offset   int      `json:"offset"`
}

func (s *Server) signCursor(v Cursor) (string, error) {
	if len(s.runtime.key) != 32 {
		return "", problem(503, "CURSOR_KEY_MISSING", "交付服务尚未配置")
	}
	data := encode(v)
	mac := hmac.New(sha256.New, s.runtime.key)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *Server) parseCursor(raw string) (Cursor, error) {
	var v Cursor
	if len(raw) > 4096 || len(s.runtime.key) != 32 {
		return v, invalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return v, invalid
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return v, invalid
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return v, invalid
	}
	mac := hmac.New(sha256.New, s.runtime.key)
	mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return v, problem(400, "INVALID_CURSOR", "分页游标无效")
	}
	v, e = bind[Cursor](data)
	if e != nil || v.Offset < 0 {
		return v, invalid
	}
	return v, nil
}
func fingerprint(v json.RawMessage) string {
	hash := sha256.Sum256(v)
	return hex.EncodeToString(hash[:])
}
func (s *Server) authorize(tx *gorm.DB, p identity.Principal, ref Resource, requested []string) (Record, GrantData, []Field, []string, error) {
	return s.authorizeDelivery(tx, p, ref, DeliveryQuery, requested)
}
func (s *Server) authorizeDelivery(tx *gorm.DB, p identity.Principal, ref Resource, method DeliveryMethod, requested []string) (Record, GrantData, []Field, []string, error) {
	if method != resourceMethod(ref) {
		return Record{}, GrantData{}, nil, nil, problem(403, "DELIVERY_METHOD_MISMATCH", "授权交付方式不匹配")
	}
	if extendedResource(ref.Type) {
		if _, e := requireSharingSnapshot(tx, ref, nil); e != nil {
			return Record{}, GrantData{}, nil, nil, e
		}
	}
	decision, e := s.securityPolicyCheck(tx, p, ref, string(method))
	if e != nil {
		return Record{}, GrantData{}, nil, nil, e
	}
	_, schema, e := resource(tx, ref, true)
	if e != nil {
		return Record{}, GrantData{}, nil, nil, e
	}
	for i := range schema {
		if slices.Contains(decision.MaskFields, schema[i].Name) {
			schema[i].Sensitive = true
		}
	}
	if admin(p) {
		fields := slices.Clone(requested)
		if len(fields) == 0 {
			for _, field := range schema {
				fields = append(fields, field.Name)
			}
		}
		if !validResourceFields(ref, fields, schema) {
			return Record{}, GrantData{}, nil, nil, invalid
		}
		// Bind pagination to this administrator; no persisted consumer grant is created.
		return Record{ID: "admin:" + p.UserID}, GrantData{}, schema, fields, nil
	}
	var grants []Record
	if e = tx.Where("kind='grant' AND owner_id=? AND state='active' AND body->'scope'->'resource'->>'resource_type'=? AND body->'scope'->'resource'->>'resource_id'=?", p.UserID, ref.Type, ref.ID).Order("created_at DESC").Find(&grants).Error; e != nil {
		return Record{}, GrantData{}, nil, nil, e
	}
	for _, candidate := range grants {
		grant, err := get(tx, "grant", candidate.ID, true)
		if err != nil {
			return Record{}, GrantData{}, nil, nil, err
		}
		v, err := decode[GrantData](grant.Body)
		if err != nil {
			return Record{}, GrantData{}, nil, nil, err
		}
		if v.Snapshot == nil || grantMethod(v.Scope) != method {
			continue
		}
		if grant.State != "active" || v.Scope.DepartmentID != p.DepartmentID || v.Scope.Resource.Version != ref.Version || !v.Scope.ExpiresAt.After(time.Now()) {
			continue
		}
		fields := requested
		if len(fields) == 0 {
			fields = slices.Clone(v.Scope.Fields)
		}
		if !validResourceFields(ref, fields, schema) {
			continue
		}
		ok := true
		for _, f := range fields {
			ok = ok && slices.Contains(v.Scope.Fields, f)
		}
		if ok {
			snapshot, err := requireSharingSnapshot(tx, ref, v.Snapshot)
			if err != nil {
				return Record{}, GrantData{}, nil, nil, err
			}
			if err = validateSharingFields(tx, snapshot, fields); err != nil {
				return Record{}, GrantData{}, nil, nil, err
			}
			schema, err = sharingSensitivity(tx, snapshot, schema)
			if err != nil {
				return Record{}, GrantData{}, nil, nil, err
			}
			return grant, v, schema, fields, nil
		}
	}
	return Record{}, GrantData{}, nil, nil, problem(403, "GRANT_REQUIRED", "没有当前账号、部门、字段和期限均有效的授权")
}
func (s *Server) RegisterDelivery(r *gin.Engine, h *identity.Handler) {
	r.POST("/delivery/v1/services/:id/invoke", s.authenticateService, s.invokeAPI)
	r.GET("/delivery/v1/services/:id/schema", s.authenticateService, s.apiDeliverySchema)
	g := r.Group("/delivery/v1", h.Authenticate, s.requireDataConsumer)
	g.GET("/datasets/:id/records", s.deliver("governed_dataset"))
	g.GET("/raw/:id/records", s.deliver("raw_object"))
	g.GET("/tables/:id/records", s.deliverTable)
	g.GET("/files/:id/content", s.deliverFile)
	g.POST("/apis/:id/invoke", h.CSRF, s.invokeAPI)
	g.GET("/apis/:id/schema", s.apiDeliverySchema)
}
func (s *Server) deliver(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		requested := []string{}
		for _, v := range c.QueryArray("fields") {
			requested = append(requested, strings.Split(v, ",")...)
		}
		ref := Resource{Type: kind, ID: c.Param("id"), Version: version}
		var response json.RawMessage
		var deliveryGrant string
		var deliveryFields []string
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			grant, _, schema, fields, err := s.authorize(tx, p, ref, requested)
			if err != nil {
				return err
			}
			deliveryGrant = grant.ID
			deliveryFields = slices.Clone(fields)
			sensitive := map[string]bool{}
			for _, field := range schema {
				sensitive[field.Name] = field.Sensitive && !admin(p)
			}
			securityDecision, securityErr := s.securityPolicyCheck(tx, p, ref, "query")
			if securityErr != nil {
				return securityErr
			}
			for _, f := range securityDecision.MaskFields {
				sensitive[f] = true
			}
			slices.Sort(fields)
			fieldHash := fingerprint(encode(fields))
			if len(securityDecision.RowFilters) > 0 {
				fieldHash = fingerprint(encode([]interface{}{fields, securityDecision.RowFilters}))
			}
			c.Set("security_row_filter_basis", fingerprint(encode(securityDecision.RowFilters)))
			start := 0
			var prior *Cursor
			if raw := c.Query("cursor"); raw != "" {
				cursor, err := s.parseCursor(raw)
				if err != nil {
					return err
				}
				if cursor.Grant != grant.ID || cursor.Resource != ref || cursor.Fields != fieldHash {
					return problem(409, "CURSOR_SCOPE_CHANGED", "游标与当前授权、资源或字段范围不匹配")
				}
				start = cursor.Offset
				prior = &cursor
			}
			var watermark json.RawMessage
			var records []DataRow
			events := []json.RawMessage{}
			rawTotal := 0
			if kind == "governed_dataset" {
				dataset, err := get(tx, "dataset", ref.ID, false)
				if err != nil {
					return err
				}
				if err = requireDatasetProcessingBasis(tx, dataset, p); err != nil {
					return err
				}
				data, err := decode[DatasetData](dataset.Body)
				if err != nil {
					return err
				}
				var quality Quality
				var rows []DataRow
				if data.GovernanceRunID != "" {
					var report governanceFlowReport
					data, quality, _, report, err = s.governanceOutputReady(c.Request.Context(), tx, dataset)
					if err != nil {
						return err
					}
					rawTotal = int(report.OutputRows)
					if start > rawTotal {
						return invalid
					}
					if len(securityDecision.RowFilters) > 0 {
						rows, rawTotal, err = s.securityGovernedFilterPage(c.Request.Context(), tx, dataset, securityDecision.RowFilters, start, limit)
					} else {
						rows, err = s.governanceOutputPage(c.Request.Context(), tx, dataset, start, limit)
					}
				} else if data.ResultBinding != nil {
					var id string
					data, quality, id, err = s.governedReady(c.Request.Context(), tx, dataset)
					if err != nil {
						return err
					}
					var snapshot ResourceSnapshot
					if err = tx.Where("id=?", id).Take(&snapshot).Error; err != nil {
						return err
					}
					rawTotal = int(snapshot.RowCount)
					if start > rawTotal {
						return invalid
					}
					rows, err = s.snapshotPage(c.Request.Context(), id, start, limit)
					if err == nil && len(rows) != min(limit, rawTotal-start) {
						return problem(409, "SNAPSHOT_COUNT_INVALID", "快照分页记录数量不一致")
					}
				} else {
					data, quality, rows, err = s.assess(c.Request.Context(), tx, dataset)
				}
				if err != nil {
					return err
				}
				if quality.Status != "passed" {
					return problem(409, "QUALITY_BLOCKED", "质量门禁阻断当前交付")
				}
				if data.ResultBinding != nil || data.GovernanceRunID != "" {
					watermark = data.LatestWatermark
					records = rows
				} else {
					var cutoff struct {
						Topic     string `json:"topic"`
						Partition int    `json:"partition"`
						End       int64  `json:"end_offset_exclusive"`
						Manifest  string `json:"manifest_id"`
					}
					if err = json.Unmarshal(data.LatestWatermark, &cutoff); err != nil {
						return err
					}
					watermark = encode(cutoff)
					records = rows
				}
			} else {
				var task Record
				if err = tx.Where("kind='task' AND body->>'object_id'=?", ref.ID).Take(&task).Error; err != nil {
					return problem(409, "RAW_NOT_ARCHIVED", "原始对象尚未归档")
				}
				data, err := decode[TaskData](task.Body)
				if err != nil {
					return err
				}
				if data.ArchiveID == "" {
					return problem(409, "RAW_NOT_ARCHIVED", "原始对象尚未归档")
				}
				archive, err := get(tx, "archive", data.ArchiveID, false)
				if err != nil {
					return err
				}
				meta, err := decode[ArchiveData](archive.Body)
				if err != nil {
					return err
				}
				watermark = encode(gin.H{"topic": meta.Topic, "partition": 0, "end_offset_exclusive": meta.End, "manifest_id": archive.ID})
				if meta.ManifestVersion == 2 {
					watermark = encode(gin.H{"topic": meta.Topic, "partitions": meta.Boundary.Partitions, "manifest_id": archive.ID})
				}
				rawTotal = meta.Count
				rows, err := s.archivePage(c.Request.Context(), meta, start, limit)
				if err != nil {
					return err
				}
				for _, row := range rows {
					event, err := decodeCaptureEvent(row.Event)
					if err != nil {
						return err
					}
					key, err := decodeCaptureKey(row.Key)
					if err != nil {
						return err
					}
					keyFields := []string{}
					for _, field := range fields {
						if _, ok := key[field]; ok {
							keyFields = append(keyFields, field)
						}
					}

					events = append(events, encode(gin.H{"event_id": row.Topic + ":" + strconv.Itoa(row.Partition) + ":" + strconv.FormatInt(row.Offset, 10), "source_id": data.SourceID, "object_id": ref.ID, "run_id": meta.RunID, "op": normalizeOp(event.Op), "key": rawProjection(key, keyFields, sensitive), "source_position": redactSource(event.Source), "source_order": nil, "ordering_guarantee": "per_key_arrival", "schema_version": ref.Version, "captured_at": time.UnixMilli(event.Timestamp).UTC(), "before": rawProjection(event.Before, fields, sensitive), "after": rawProjection(event.After, fields, sensitive), "trace_id": c.GetString("trace_id")}))
				}
			}
			cutoffHash := fingerprint(watermark)
			if prior != nil && prior.Cutoff != cutoffHash {
				return problem(409, "CURSOR_WATERMARK_CHANGED", "资源截止范围已变化，请从第一页重新查询")
			}
			total := len(records)
			if kind == "raw_object" || (kind == "governed_dataset" && rawTotal > 0) {
				total = rawTotal
			}
			if start > total {
				return invalid
			}
			end := min(start+limit, total)
			var next *string
			if end < total {
				cursor, err := s.signCursor(Cursor{Grant: grant.ID, Resource: ref, Fields: fieldHash, Cutoff: cutoffHash, Offset: end})
				if err != nil {
					return err
				}
				next = &cursor
			}
			if kind == "governed_dataset" {
				out := []DataRow{}
				selected := records
				if rawTotal == 0 {
					selected = records[start:end]
				}
				for _, row := range selected {
					out = append(out, rawProjection(row, fields, sensitive))
				}
				response = encode(gin.H{"resource": ref, "watermark": watermark, "records": out, "next_cursor": next})
			} else {
				response = encode(gin.H{"resource": ref, "cutoff": watermark, "events": events, "next_cursor": next})
			}
			return nil
		})
		if e != nil {
			s.fail(c, e)
			return
		}
		if err := s.securitySend(c, p, ref, "query", deliveryGrant, deliveryFields, response, "application/json"); err != nil && !c.Writer.Written() {
			s.fail(c, err)
		}
	}
}
