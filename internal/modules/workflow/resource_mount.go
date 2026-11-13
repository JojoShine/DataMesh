package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Only immutable execution evidence can make an additional mount share the source catalog.
func governanceVariantMatches(directoryID string, record Record, data, proof DatasetData, run GovernanceRun) bool {
	g := data.Governance
	return g != nil && g.SingleSource && g.Source != nil && g.OriginalDirectoryID != "" && g.OriginalDirectoryID == directoryID && g.ProviderDepartmentID == record.DepartmentID &&
		data.GovernanceRunID != "" && data.GovernanceRunID == run.ID && record.ID == run.ID && record.Version == 1 && record.OwnerID == run.ActorID && run.Kind == "dataflow" && run.State == "succeeded" &&
		fingerprint(encode(data)) == fingerprint(encode(proof))
}

func governanceMountVariant(tx *gorm.DB, directoryID string, ref Resource) (bool, error) {
	if ref.Type != "governed_dataset" {
		return false, nil
	}
	r, err := get(tx, "dataset", ref.ID, false)
	if err != nil {
		return false, err
	}
	data, err := decode[DatasetData](r.Body)
	if err != nil {
		return false, err
	}
	if data.Governance == nil || !data.Governance.SingleSource || data.Governance.OriginalDirectoryID != directoryID {
		return false, nil
	}
	var proof struct{ DatasetBody json.RawMessage }
	if err = tx.Table("governance_outputs").Where("run_id=? AND dataset_id=?", data.GovernanceRunID, r.ID).Take(&proof).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果没有可信执行登记证据")
		}
		return false, err
	}
	proofData, err := decode[DatasetData](proof.DatasetBody)
	if err != nil {
		return false, err
	}
	var run GovernanceRun
	if err = tx.Where("id=?", data.GovernanceRunID).Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果运行证据不可用")
		}
		return false, err
	}
	if ref.Version != r.Version || !governanceVariantMatches(directoryID, r, data, proofData, run) {
		return false, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果归属或治理内容与不可变登记证据不符")
	}
	return true, nil
}

type MountInput struct {
	DirectoryID     string            `json:"directory_id"`
	Resource        Resource          `json:"resource"`
	Mapping         map[string]string `json:"mapping"`
	ExpectedVersion int               `json:"expected_version"`
}

func mountView(tx *gorm.DB, r Record, p identity.Principal) (json.RawMessage, error) {
	d, e := decode[MountData](r.Body)
	if e != nil {
		return nil, e
	}
	rev, e := get(tx, "mount_revision", d.DraftID, false)
	if e != nil {
		return nil, e
	}
	v, e := decode[MountRevisionData](rev.Body)
	if e != nil {
		return nil, e
	}
	permissionErr := requireResourceCapability(tx, p, Resource{Type: "mount", ID: r.ID}, "operate_resource")
	if permissionErr != nil {
		if _, ok := permissionErr.(*Problem); !ok {
			return nil, permissionErr
		}
	}
	return encode(gin.H{"mount_id": r.ID, "directory_id": r.ParentID, "state": r.State, "version": r.Version, "revision_id": rev.ID, "revision_state": rev.State, "active_revision_id": d.ActiveID, "definition": v, "can_operate": permissionErr == nil}), nil
}
func (s *Server) registerResourceMount(api, write *gin.RouterGroup) {
	api.GET("/resource-mounts", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
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
		q := tx.Model(&Record{}).Where("kind='mount'")
		if !admin(p) {
			q = q.Where("department_id=?", p.DepartmentID)
		}
		if id := c.Query("directory_id"); id != "" {
			q = q.Where("parent_id=?", id)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e = q.Order("updated_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, r := range rows {
			v, err := mountView(tx, r, p)
			if err != nil {
				s.fail(c, err)
				return
			}
			items = append(items, v)
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	})
	api.GET("/resource-mounts/:id", s.require("exchange"), func(c *gin.Context) {
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		r, e := get(tx, "mount", c.Param("id"), false)
		if e == nil && !department(p, r) {
			e = denied
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		v, e := mountView(tx, r, p)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.Data(200, "application/json", v)
	})
	write.POST("/resource-mounts", s.require("exchange"), s.mutate("create_mount", s.createMount))
	for _, a := range []string{"revisions", "submit", "withdraw-submission", "suspend", "unmount"} {
		action := a
		write.POST("/resource-mounts/:id/"+action, s.require("exchange"), s.mutatePath("mount_"+action, s.mountAction(action, nil, "")))
	}
	for _, a := range []string{"validate", "activate"} {
		action := a
		write.POST("/resource-mounts/:id/"+action, s.require("exchange"), func(c *gin.Context) {
			tx := s.db.WithContext(c.Request.Context())
			p := principal(c)
			r, e := get(tx, "mount", c.Param("id"), false)
			if e == nil {
				e = requireResourceCapability(tx, p, Resource{Type: "mount", ID: r.ID}, "operate_resource")
			}
			if e != nil {
				s.fail(c, e)
				return
			}
			d, e := decode[MountData](r.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			rev, e := get(tx, "mount_revision", d.DraftID, false)
			if e != nil {
				s.fail(c, e)
				return
			}
			v, e := decode[MountRevisionData](rev.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			report, e := s.validateMount(c.Request.Context(), tx, r.ParentID, v, p)
			if e != nil {
				s.fail(c, e)
				return
			}
			s.mutatePath("mount_"+action, s.mountAction(action, &report, rev.ID))(c)
		})
	}
}
func mountDefinition(tx *gorm.DB, p identity.Principal, input MountInput) (Record, MountRevisionData, error) {
	d, e := get(tx, "directory", input.DirectoryID, false)
	if e != nil {
		return d, MountRevisionData{}, e
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "directory", ID: d.ID}, "operate_resource"); e != nil {
		return d, MountRevisionData{}, e
	}
	dv, e := decode[DirectoryData](d.Body)
	if e != nil {
		return d, MountRevisionData{}, e
	}
	revisionID := dv.PublishedID
	if revisionID == "" {
		revisionID = dv.DraftID
	}
	if revisionID == "" {
		return d, MountRevisionData{}, conflict
	}
	r, _, e := resource(tx, input.Resource, false)
	if e != nil {
		return d, MountRevisionData{}, e
	}
	if r.DepartmentID != d.DepartmentID {
		return d, MountRevisionData{}, denied
	}
	if e = requireResourceCapability(tx, p, input.Resource, "operate_resource"); e != nil {
		return d, MountRevisionData{}, e
	}
	v := MountRevisionData{DirectoryRevisionID: revisionID, Resource: input.Resource, Mapping: input.Mapping}
	return d, v, validateMountSchema(tx, d.ID, v, false)
}
func validateMountSchema(tx *gorm.DB, directoryID string, v MountRevisionData, requirePublished bool) error {
	d, e := get(tx, "directory", directoryID, false)
	if e != nil {
		return e
	}
	data, e := decode[DirectoryData](d.Body)
	if e != nil {
		return e
	}
	revisionID := data.PublishedID
	if !requirePublished && revisionID == "" {
		revisionID = data.DraftID
	}
	if (requirePublished && d.State != "published") || revisionID != v.DirectoryRevisionID {
		return problem(409, "DIRECTORY_REVISION_CHANGED", "目录未发布或修订已变化，请重新挂载")
	}
	rev, e := get(tx, "directory_revision", revisionID, false)
	if e != nil {
		return e
	}
	dv, e := decode[DirectoryRevisionData](rev.Body)
	if e != nil {
		return e
	}
	layer, err := resourceLayer(tx, v.Resource)
	if err != nil {
		return err
	}
	format := "database"
	if v.Resource.Type == "file_resource" {
		format = "file"
	}
	if v.Resource.Type == "api_resource" {
		format = "api"
	}
	variant, err := governanceMountVariant(tx, directoryID, v.Resource)
	if err != nil {
		return err
	}
	serviceVariant, err := serviceMountVariant(tx, directoryID, v.Resource)
	if err != nil {
		return err
	}
	layerMatches := dv.Definition.DataLayer == layer || (variant && dv.Definition.DataLayer == "raw_object" && layer == "governed_dataset")
	if !serviceVariant && (!layerMatches || dv.Definition.Format != format || dv.Definition.SharingMethod != string(resourceMethod(v.Resource))) {
		return problem(409, "MOUNT_TYPE_UNAVAILABLE", "目录数据层、载体或交付方式与资源不一致")
	}

	r, fields, e := resource(tx, v.Resource, false)
	if e != nil {
		return e
	}
	if r.DepartmentID != d.DepartmentID {
		return denied
	}
	if v.Resource.Type == "file_resource" {
		if len(v.Mapping) != 0 {
			return invalid
		}
		return nil
	}
	if (serviceVariant && len(v.Mapping) != len(fields)) || (!serviceVariant && len(v.Mapping) != len(dv.Definition.Items)) || len(v.Mapping) == 0 {
		return problem(400, "MAPPING_REQUIRED", "请为全部目录数据项配置唯一字段映射")
	}
	seen := map[string]bool{}
	hasID := false
	for _, item := range dv.Definition.Items {
		target := v.Mapping[item.Name]
		if serviceVariant && target == "" {
			continue
		}
		found := false
		for _, f := range fields {
			if f.Name == target {
				found = true
				if f.LogicalType != item.LogicalType || (f.Sensitive && !item.Sensitive) {
					return problem(400, "FIELD_POLICY_MISMATCH", "字段类型或敏感标记与实际资源不一致")
				}
			}
		}
		if !found || seen[target] {
			return invalid
		}
		seen[target] = true
		hasID = hasID || target == "id"
	}
	if serviceVariant && len(seen) != len(fields) {
		return problem(400, "MAPPING_REQUIRED", "请为接口返回的每个字段配置唯一目录映射")
	}
	if v.Resource.Type == "raw_object" && !hasID {
		return problem(400, "PRIMARY_KEY_REQUIRED", "原始交换映射须包含主键 id")
	}
	return nil
}
func (s *Server) createMount(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[MountInput](b)
	if e != nil {
		return 0, nil, "", e
	}
	d, rv, e := mountDefinition(tx, p, v)
	if e != nil {
		return 0, nil, "", e
	}
	r, e := newRecord("mount", d.DepartmentID, p.UserID, d.ID, "draft", MountData{})
	if e != nil {
		return 0, nil, "", e
	}
	rev, e := newRecord("mount_revision", d.DepartmentID, p.UserID, r.ID, "draft", rv)
	if e != nil {
		return 0, nil, "", e
	}
	variant, e := governanceMountVariant(tx, d.ID, rv.Resource)
	if e != nil {
		return 0, nil, "", e
	}
	serviceVariant, e := serviceMountVariant(tx, d.ID, rv.Resource)
	if e != nil {
		return 0, nil, "", e
	}
	r.Body = encode(MountData{DraftID: rev.ID, ResourceKey: resourceKey(rv.Resource), GovernanceVariant: variant, ServiceVariant: serviceVariant})
	if e = tx.Create(&r).Error; e == nil {
		e = tx.Create(&rev).Error
	}
	if e != nil {
		return 0, nil, "", e
	}
	out, e := mountView(tx, r, p)
	return 201, out, r.ID, e
}
func (s *Server) validateMount(ctx context.Context, tx *gorm.DB, directoryID string, v MountRevisionData, p identity.Principal) (MountValidation, error) {
	out := MountValidation{CheckedAt: time.Now().UTC(), Passed: false, Checks: []MountCheck{}, Watermark: json.RawMessage("null")}
	check := func(name string, e error) bool {
		message := "已通过"
		if e != nil {
			message = "运行服务不可用，请核对实际处理状态"
			if pe, ok := e.(*Problem); ok {
				message = pe.Message
			}
		}
		out.Checks = append(out.Checks, MountCheck{Name: name, Passed: e == nil, Message: message})
		return e == nil
	}
	if !check("目录与字段映射", validateMountSchema(tx, directoryID, v, true)) {
		return out, nil
	}
	r, _, e := resource(tx, v.Resource, false)
	if e != nil {
		return out, e
	}
	if v.Resource.Type == "table_resource" {
		data, err := decode[ManagedResource](r.Body)
		if err != nil {
			return out, err
		}
		fields := []string{}
		for _, f := range data.Fields {
			fields = append(fields, f.Name)
		}
		_, _, err = s.readTable(ctx, tx, data, fields, nil, 1, true)
		if !check("库表实际读取与稳定排序键", err) {
			return out, nil
		}
	} else if v.Resource.Type == "file_resource" {
		data, err := decode[ManagedResource](r.Body)
		if err != nil {
			return out, err
		}
		cfg, err := decode[FileConfig](data.Config)
		if err != nil {
			return out, err
		}
		_, err = s.readStoredFile(ctx, cfg, 50<<20)
		if !check("文件实际读取与摘要", err) {
			return out, nil
		}
	} else if v.Resource.Type == "api_resource" {
		data, err := decode[ManagedResource](r.Body)
		if err != nil {
			return out, err
		}
		cfg, err := decode[APIConfig](data.Config)
		if err != nil {
			return out, err
		}
		if !allowAPICall(p.UserID + ":" + r.ID) {
			check("API 调用频率", problem(429, "API_RATE_LIMIT", "调用频率超过限制"))
			return out, nil
		}
		_, err = s.callAPI(ctx, cfg, data.Fields, cfg.TestParameters)
		if !check("API 实际调用与响应结构", err) {
			return out, nil
		}
	} else if extendedResource(v.Resource.Type) {
		check("资源交付适配器", problem(409, "ADAPTER_UNAVAILABLE", "资源交付适配器尚未准备完成"))
		return out, nil
	} else if v.Resource.Type == "governed_dataset" {
		if !check("来源加工及成果共享依据", requireDatasetProcessingBasis(tx, r, p)) {
			return out, nil
		}
		data, q, _, err := s.assessQuality(ctx, tx, r)
		if !check("治理结果与完整性", err) {
			return out, nil
		}
		if q.Status != "passed" {
			check("质量门禁", problem(409, "QUALITY_BLOCKED", "治理质量未通过"))
			return out, nil
		}
		check("质量门禁", nil)
		out.Watermark = data.LatestWatermark
	} else {
		var task Record
		e = tx.Where("kind='task' AND body->>'object_id'=?", r.ID).Take(&task).Error
		if e != nil {
			check("原始归档", problem(409, "ARCHIVE_REQUIRED", "尚未建立可交付的原始归档"))
			return out, nil
		}
		td, err := decode[TaskData](task.Body)
		if err != nil {
			return out, err
		}
		if td.ArchiveID == "" {
			check("原始归档", problem(409, "ARCHIVE_REQUIRED", "请先归档原始数据"))
			return out, nil
		}
		a, err := get(tx, "archive", td.ArchiveID, false)
		if err != nil {
			return out, err
		}
		meta, err := decode[ArchiveData](a.Body)
		if err != nil {
			return out, err
		}
		_, err = s.rawRecords(ctx, meta)
		if !check("原始归档可读取", err) {
			return out, nil
		}
		out.Watermark = encode(gin.H{"manifest_id": a.ID, "end_offset_exclusive": meta.End})
	}
	out.Passed = true
	return out, nil
}
func (s *Server) mountAction(action string, report *MountValidation, checkedRevision string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		// Lock a physical resource before its directory and mount, consistently with delivery.
		initial, e := get(tx, "mount", id, false)
		if e != nil {
			return 0, nil, id, e
		}
		initialData, e := decode[MountData](initial.Body)
		if e != nil {
			return 0, nil, id, e
		}
		initialRev, e := get(tx, "mount_revision", initialData.DraftID, false)
		if e != nil {
			return 0, nil, id, e
		}
		initialValue, e := decode[MountRevisionData](initialRev.Body)
		if e != nil {
			return 0, nil, id, e
		}
		if _, _, e = resource(tx, initialValue.Resource, true); e != nil && !slices.Contains([]string{"suspend", "unmount"}, action) {
			return 0, nil, id, e
		}
		if _, e = get(tx, "directory", initial.ParentID, true); e != nil {
			return 0, nil, id, e
		}
		r, e := get(tx, "mount", id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if e = requireResourceCapability(tx, p, Resource{Type: "mount", ID: id}, "operate_resource"); e != nil {
			return 0, nil, id, e
		}
		data, e := decode[MountData](r.Body)
		if e != nil {
			return 0, nil, id, e
		}
		rev, e := get(tx, "mount_revision", data.DraftID, true)
		if e != nil {
			return 0, nil, id, e
		}
		value, e := decode[MountRevisionData](rev.Body)
		if e != nil {
			return 0, nil, id, e
		}
		req := VersionAction{}
		if action == "revisions" {
			input, err := bind[MountInput](b)
			if err != nil {
				return 0, nil, id, err
			}
			req.ExpectedVersion = input.ExpectedVersion
			if input.DirectoryID != r.ParentID || slices.Contains([]string{"pending"}, rev.State) {
				return 0, nil, id, conflict
			}
			_, rv, err := mountDefinition(tx, p, input)
			if err != nil {
				return 0, nil, id, err
			}
			next, err := newRecord("mount_revision", r.DepartmentID, p.UserID, id, "draft", rv)
			if err != nil {
				return 0, nil, id, err
			}
			if r.Version != req.ExpectedVersion {
				return 0, nil, id, conflict
			}
			if err = tx.Create(&next).Error; err != nil {
				return 0, nil, id, err
			}
			data.DraftID = next.ID
			activeResource := rv.Resource
			if r.State == "active" {
				active, err := get(tx, "mount_revision", data.ActiveID, false)
				if err != nil {
					return 0, nil, id, err
				}
				activeValue, err := decode[MountRevisionData](active.Body)
				if err != nil {
					return 0, nil, id, err
				}
				activeResource = activeValue.Resource
			}
			data.ServiceVariant, err = serviceMountVariant(tx, r.ParentID, activeResource)
			if err != nil {
				return 0, nil, id, err
			}
			data.GovernanceVariant, err = governanceMountVariant(tx, r.ParentID, activeResource)
			if err != nil {
				return 0, nil, id, err
			}
		} else {
			req, e = bind[VersionAction](b)
			if e != nil {
				return 0, nil, id, e
			}
			if r.Version != req.ExpectedVersion {
				return 0, nil, id, conflict
			}
			switch action {
			case "validate", "activate":
				if report == nil || rev.ID != checkedRevision || time.Since(report.CheckedAt) > 15*time.Minute {
					return 0, nil, id, conflict
				}
				if e = validateMountSchema(tx, r.ParentID, value, true); e != nil {
					return 0, nil, id, e
				}
				if action == "validate" {
					if !slices.Contains([]string{"draft", "validated"}, rev.State) {
						return 0, nil, id, conflict
					}
					value.Validation = report
					rev.State = "draft"
					if report.Passed {
						rev.State = "validated"
					}
				} else {
					if rev.State != "approved" || !report.Passed {
						return 0, nil, id, problem(409, "MOUNT_NOT_READY", "挂载须审核通过且实时校验通过后才能启用")
					}
					if value.Validation == nil || !value.Validation.Passed || time.Since(value.Validation.CheckedAt) > 15*time.Minute {
						return 0, nil, id, problem(409, "VALIDATION_EXPIRED", "校验报告已过期，请新建修订重新校验审核")
					}
					value.Validation = report
					data.ActiveID = rev.ID
					data.ResourceKey = resourceKey(value.Resource)
					data.ServiceVariant, e = serviceMountVariant(tx, r.ParentID, value.Resource)
					if e != nil {
						return 0, nil, id, e
					}
					data.GovernanceVariant, e = governanceMountVariant(tx, r.ParentID, value.Resource)
					if e != nil {
						return 0, nil, id, e
					}
					r.State = "active"
				}
			case "submit":
				if rev.State != "validated" || value.Validation == nil || !value.Validation.Passed || time.Since(value.Validation.CheckedAt) > 15*time.Minute {
					return 0, nil, id, problem(409, "VALIDATION_REQUIRED", "请先执行有效的挂载校验")
				}
				if e = validateMountSchema(tx, r.ParentID, value, true); e != nil {
					return 0, nil, id, e
				}
				if e = hasReviewer(tx, r.DepartmentID, "mount_review", p.UserID, "", r.ID); e != nil {
					return 0, nil, id, e
				}
				value.SubmittedBy = p.UserID
				rev.State = "pending"
				if e = createReview(tx, r, rev, "mount", p.UserID); e != nil {
					return 0, nil, id, e
				}
			case "withdraw-submission":
				if rev.State != "pending" || (value.SubmittedBy != p.UserID && !admin(p)) || !valid(req.Reason, 1000) {
					return 0, nil, id, conflict
				}
				if e = cancelReviews(tx, rev.ID); e != nil {
					return 0, nil, id, e
				}
				rev.State = "withdrawn"
				value.Reason = req.Reason
			case "suspend", "unmount":
				if r.State != "active" && r.State != "suspended" {
					return 0, nil, id, conflict
				}
				if !valid(req.Reason, 1000) {
					return 0, nil, id, invalid
				}
				r.State = "suspended"
				if action == "unmount" {
					r.State = "unmounted"
				}
				value.Reason = req.Reason
			default:
				return 0, nil, id, invalid
			}
			rev.Body = encode(value)
			rev.Version++
			if e = save(tx, &rev); e != nil {
				return 0, nil, id, e
			}
		}
		r.Body = encode(data)
		r.Version++
		if e = save(tx, &r); e != nil {
			return 0, nil, id, e
		}
		out, e := mountView(tx, r, p)
		return 200, out, id, e
	}
}
