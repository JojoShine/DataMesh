package workflow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"datamesh/internal/modules/identity"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

func securitySigningKey(id string) (string, []byte, error) {
	if id == "" {
		id = os.Getenv("SECURITY_WATERMARK_KEY_ID")
	}
	var keys map[string]string
	if id == "" || json.Unmarshal([]byte(os.Getenv("SECURITY_WATERMARK_KEYS")), &keys) != nil || len(keys) > 8 {
		return id, nil, problem(503, "SECURITY_SIGNING_UNAVAILABLE", "水印签名密钥未配置，无法生成所需交付凭据")
	}
	key, e := hex.DecodeString(keys[id])
	if e != nil || len(key) != 32 {
		return id, nil, problem(503, "SECURITY_SIGNING_KEY_UNAVAILABLE", "所需版本的签名密钥不可用，不能将此凭据判定为有效")
	}
	return id, key, nil
}
func securityCanonical(raw json.RawMessage) json.RawMessage {
	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return json.RawMessage("null")
	}
	return encode(value)
}
func securityProofPayload(p SecurityDeliveryProof) json.RawMessage {
	return encode(struct {
		ID, ActorID, DepartmentID, Action, GrantID, Text, SHA256, KeyID, RunID string
		Resource, Fields, Controls, Manifest                                   json.RawMessage
		Size                                                                   int64
		CreatedAt                                                              string
	}{p.ID, p.ActorID, p.DepartmentID, p.Action, p.GrantID, p.Text, p.SHA256, p.KeyID, p.RunID, securityCanonical(p.Resource), securityCanonical(p.Fields), securityCanonical(p.Controls), securityCanonical(p.Manifest), p.SizeBytes, p.CreatedAt.UTC().Format(time.RFC3339Nano)})
}
func securityProofSignature(p SecurityDeliveryProof, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(securityProofPayload(p))
	return hex.EncodeToString(mac.Sum(nil))
}
func securityRender(text string, p identity.Principal, ref Resource, id string, now time.Time) string {
	return strings.NewReplacer("{recipient}", p.Username, "{department}", p.DepartmentID, "{resource}", ref.ID, "{version}", strings.TrimSpace(string(encode(ref.Version))), "{time}", now.UTC().Format(time.RFC3339), "{delivery_id}", id).Replace(text)
}
func (s *Server) securityPrepareProof(tx *gorm.DB, p identity.Principal, ref Resource, action, grant string, fields []string, body []byte, decision SecurityDecision, runID string) (*SecurityDeliveryProof, error) {
	if !decision.RequireProof && !decision.Visible && !decision.FileMark && !decision.Invisible {
		return nil, nil
	}
	id, e := newID()
	if e != nil {
		return nil, e
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	keyID, key, e := securitySigningKey("")
	if e != nil {
		return nil, e
	}
	sorted := slices.Clone(fields)
	slices.Sort(sorted)
	if sorted == nil {
		sorted = []string{}
	}
	proof := SecurityDeliveryProof{Manifest: json.RawMessage("{}"), ID: id, ActorID: p.UserID, DepartmentID: p.DepartmentID, Resource: encode(ref), Action: action, GrantID: grant, Fields: encode(sorted), Controls: encode(decision.Controls), Text: securityRender(decision.Text, p, ref, id, now), KeyID: keyID, State: "prepared", RunID: runID, CreatedAt: now, UpdatedAt: now}
	if body != nil {
		sum := sha256.Sum256(body)
		proof.SHA256 = hex.EncodeToString(sum[:])
		proof.SizeBytes = int64(len(body))
		proof.Signature = securityProofSignature(proof, key)
	}
	if e = tx.Create(&proof).Error; e != nil {
		return nil, e
	}
	return &proof, nil
}
func (s *Server) securitySend(c *gin.Context, p identity.Principal, ref Resource, action, grant string, fields []string, body []byte, contentType string) error {
	live, err := s.securityDeliveryPrincipal(c.Request.Context(), p.UserID)
	if err != nil {
		return err
	}
	p = live
	tx := s.db.WithContext(c.Request.Context())
	auditAction := "deliver_" + ref.Type
	if ref.Type == "table_resource" {
		auditAction = "deliver_table"
	}
	if ref.Type == "api_resource" {
		auditAction = "invoke_api"
	}
	var proof *SecurityDeliveryProof
	var d SecurityDecision
	e := tx.Transaction(func(t *gorm.DB) error {
		var err error
		currentGrant, _, _, _, authErr := s.authorizeDelivery(t, p, ref, resourceMethod(ref), fields)
		if authErr != nil {
			return authErr
		}
		if grant != "" && currentGrant.ID != grant {
			return problem(409, "SECURITY_AUTHORIZATION_CHANGED", "交付前授权已变化，请重新请求")
		}
		if key := c.GetString("service_key_id"); key != "" {
			var count int64
			if err := t.Model(&DataServiceKey{}).Where("id=? AND NOT revoked AND expires_at>now()", key).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 || currentGrant.ID != c.GetString("service_key_grant") {
				return denied
			}
		}
		grant = currentGrant.ID
		d, err = s.securityPolicyCheck(t, p, ref, action)
		if err != nil {
			return err
		}
		if action == "download" && (d.FileMark || d.Invisible) {
			return problem(409, "SECURITY_CONTENT_MARK_REQUIRED", "此文件必须先生成水印副本，不能直接返回原件")
		}
		if len(d.RowFilters) > 0 && c.GetString("security_row_filter_basis") != fingerprint(encode(d.RowFilters)) {
			return problem(409, "SECURITY_FILTER_CHANGED", "交付前行过滤条件已变化，请重新请求")
		}
		body, err = securityMaskPayload(body, d.MaskFields)
		if err != nil {
			return err
		}
		proof, err = s.securityPrepareProof(t, p, ref, action, grant, fields, body, d, "")
		if err != nil {
			return err
		}
		if proof != nil && d.Invisible {
			_, key, keyErr := securitySigningKey(proof.KeyID)
			if keyErr != nil {
				return keyErr
			}
			body, err = securityTraceJSON(body, securityTraceToken(*proof, key))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			proof.SHA256 = hex.EncodeToString(sum[:])
			proof.SizeBytes = int64(len(body))
			proof.Signature = securityProofSignature(*proof, key)
			if err = t.Save(proof).Error; err != nil {
				return err
			}
		}
		if proof != nil {
			if err = s.audit(t, p, "security_delivery_prepared", proof.ID, "success", c.GetString("trace_id")); err != nil {
				return err
			}
		}
		return s.audit(t, p, auditAction, ref.ID, "started", c.GetString("trace_id"))
	})
	if e != nil {
		return e
	}
	if proof != nil {
		c.Header("X-DataMesh-Delivery-ID", proof.ID)
		if d.Visible {
			c.Header("X-DataMesh-Watermark", url.QueryEscape(proof.Text))
		}
	}
	c.Header("Content-Type", contentType)
	c.Status(200)
	n, writeErr := c.Writer.Write(body)
	state, result := "completed", "success"
	if writeErr != nil || n != len(body) {
		state = "interrupted"
		result = "interrupted"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	finalErr := s.db.WithContext(ctx).Transaction(func(t *gorm.DB) error {
		if err := s.audit(t, p, auditAction, ref.ID, result, c.GetString("trace_id")); err != nil {
			return err
		}
		if proof != nil {
			return t.Model(&SecurityDeliveryProof{}).Where("id=? AND state='prepared'", proof.ID).Updates(map[string]interface{}{"state": state, "updated_at": time.Now().UTC()}).Error
		}
		return nil
	})
	if finalErr != nil {
		c.Error(finalErr)
	}
	return writeErr
}
func securityProofAccess(tx *gorm.DB, p identity.Principal, id string) (SecurityDeliveryProof, error) {
	var proof SecurityDeliveryProof
	e := tx.Where("id=?", id).Take(&proof).Error
	if e != nil {
		return proof, e
	}
	if proof.ActorID != p.UserID && !(platform(p, "security") && role(p, "admin", "auditor")) {
		return proof, denied
	}
	return proof, nil
}
func (s *Server) securityProofDetail(c *gin.Context) {
	proof, e := securityProofAccess(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"))
	if e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, proof)
}
func (s *Server) securityProofs(c *gin.Context) {
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
	q := s.db.WithContext(c.Request.Context()).Model(&SecurityDeliveryProof{})
	if id := c.Query("resource_id"); id != "" {
		q = q.Where("resource->>'resource_id'=?", id)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []SecurityDeliveryProof{}
	if e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
func (s *Server) securityVerifyProof(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		SHA256 string `json:"sha256"`
	}](b)
	if e != nil || len(input.SHA256) != 64 {
		return 0, nil, "", invalid
	}
	if _, e = hex.DecodeString(input.SHA256); e != nil {
		return 0, nil, "", invalid
	}
	proof, e := securityProofAccess(tx, p, id)
	if e != nil {
		return 0, nil, "", e
	}
	if proof.Signature == "" || proof.SHA256 == "" {
		return 200, encode(gin.H{"result": "unverifiable", "reason": "凭据尚未完成内容摘要与签名，不能作为完成交付的证明"}), id, nil
	}
	_, key, e := securitySigningKey(proof.KeyID)
	if e != nil {
		return 200, encode(gin.H{"result": "unverifiable", "reason": "签名密钥版本不可用"}), id, nil
	}
	expected := securityProofSignature(proof, key)
	result := "matched"
	if !hmac.Equal([]byte(expected), []byte(proof.Signature)) {
		result = "invalid_signature"
	} else if !hmac.Equal([]byte(strings.ToLower(input.SHA256)), []byte(proof.SHA256)) {
		result = "content_changed"
	}
	return 200, encode(gin.H{"result": result, "delivery_state": proof.State, "delivery_id": proof.ID, "notice": "匹配表示摘要与签名记录一致，不单独证明接收完成或责任归属"}), id, nil
}

func securityMaskPayload(body []byte, fields []string) ([]byte, error) {
	if len(fields) == 0 {
		return body, nil
	}
	var root map[string]json.RawMessage
	if e := json.Unmarshal(body, &root); e != nil {
		return nil, e
	}
	mask := func(raw json.RawMessage) (json.RawMessage, error) {
		if string(raw) == "null" || len(raw) == 0 {
			return raw, nil
		}
		var row map[string]json.RawMessage
		if e := json.Unmarshal(raw, &row); e != nil {
			return nil, e
		}
		for _, field := range fields {
			if v, ok := row[field]; ok && string(v) != "null" {
				row[field] = encode("***")
			}
		}
		return encode(row), nil
	}
	if raw, ok := root["records"]; ok {
		var rows []json.RawMessage
		if e := json.Unmarshal(raw, &rows); e != nil {
			return nil, e
		}
		for i, row := range rows {
			v, e := mask(row)
			if e != nil {
				return nil, e
			}
			rows[i] = v
		}
		root["records"] = encode(rows)
	}
	if raw, ok := root["events"]; ok {
		var events []map[string]json.RawMessage
		if e := json.Unmarshal(raw, &events); e != nil {
			return nil, e
		}
		for _, event := range events {
			for _, name := range []string{"before", "after", "key"} {
				if row, ok := event[name]; ok {
					v, e := mask(row)
					if e != nil {
						return nil, e
					}
					event[name] = v
				}
			}
		}
		root["events"] = encode(events)
	}
	return encode(root), nil
}

func (s *Server) securityETLProof(ctx context.Context, p identity.Principal, run *ETLRun, id string) (*SecurityDeliveryProof, error) {
	if run.Definition.SourceResource == nil {
		return nil, nil
	}
	tx := s.db.WithContext(ctx)
	live, err := s.etlPrincipal(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	p = live
	if err = s.etlPermission(tx, p, run.Definition, run.authorization); err != nil {
		return nil, err
	}
	ref := *run.Definition.SourceResource
	d, e := s.securityPolicyCheck(tx, p, ref, "etl")
	if e != nil {
		return nil, e
	}
	// This digest binds the execution manifest, not the entire destination table.
	manifest := securityCanonical(encode(gin.H{"run_id": id, "input_snapshot": run.InputSnapshot, "target_source_id": run.Definition.Target.SourceID, "target_schema": run.Definition.Target.Schema, "target_table": run.Definition.Target.Table, "read_rows": run.Read, "scope": "目标事务提交前的交付清单，不是目标表完整内容摘要"}))
	fields := []string{}
	for _, m := range run.Definition.Mapping {
		fields = append(fields, m.Source)
	}
	grant := ""
	if run.authorization != nil {
		grant = run.authorization.GrantID
	}
	var proof *SecurityDeliveryProof
	e = tx.Transaction(func(t *gorm.DB) error {
		var err error
		proof, err = s.securityPrepareProof(t, p, ref, "etl", grant, fields, manifest, d, id)
		if err != nil {
			return err
		}
		if proof != nil {
			proof.Manifest = manifest
			_, key, err := securitySigningKey(proof.KeyID)
			if err != nil {
				return err
			}
			proof.Signature = securityProofSignature(*proof, key)
			if err = t.Save(proof).Error; err != nil {
				return err
			}
			return s.audit(t, p, "security_etl_prepared", proof.ID, "success", id)
		}
		return nil
	})
	return proof, e
}

func (s *Server) securityDeliveryPrincipal(ctx context.Context, id string) (identity.Principal, error) {
	p, e := s.etlPrincipal(ctx, id)
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return p, denied
	}
	if e != nil {
		return p, e
	}
	if !platform(p, "exchange") && !platform(p, "assets") {
		return p, denied
	}
	return p, nil
}
