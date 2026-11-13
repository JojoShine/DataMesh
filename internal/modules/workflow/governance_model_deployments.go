package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceModelDeployment struct {
	ID                string    `json:"id" gorm:"primaryKey"`
	DepartmentID      string    `json:"department_id"`
	ActorID           string    `json:"actor_id"`
	ModelID           string    `json:"model_id"`
	ModelVersion      int       `json:"model_version"`
	SourceID          string    `json:"source_id"`
	SourceVersion     int       `json:"source_version"`
	SourceFingerprint string    `json:"-"`
	TargetSchema      string    `json:"target_schema"`
	TargetTable       string    `json:"target_table"`
	DDL               string    `json:"ddl"`
	State             string    `json:"state"`
	ErrorCode         string    `json:"error_code"`
	LeaseOwner        string    `json:"-"`
	LeaseExpiresAt    time.Time `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (GovernanceModelDeployment) TableName() string { return "governance_model_deployments" }

type governanceModelTarget struct {
	SourceID      string `json:"source_id"`
	SourceVersion int    `json:"source_version"`
	PreviewToken  string `json:"preview_token"`
	ConfirmTable  string `json:"confirm_table"`
}

func (s *Server) governanceModelBasis(tx *gorm.DB, p identity.Principal, ref GovernanceRef, input governanceModelTarget) (GovernanceModelDeployment, GovernanceModel, SourceSecret, *sql.DB, error) {
	job := GovernanceModelDeployment{ModelID: ref.ID, ModelVersion: ref.Version, SourceID: input.SourceID, SourceVersion: input.SourceVersion, ActorID: p.UserID}
	head, version, e := governanceRead(tx, p, "model", ref.ID, ref.Version)
	if e != nil {
		return job, GovernanceModel{}, SourceSecret{}, nil, e
	}
	if version.State != "published" {
		return job, GovernanceModel{}, SourceSecret{}, nil, conflict
	}
	def, e := decode[GovernanceDefinition](version.Definition)
	if e != nil {
		return job, GovernanceModel{}, SourceSecret{}, nil, e
	}
	if def.Model == nil || def.Model.Layer != "physical" {
		return job, GovernanceModel{}, SourceSecret{}, nil, invalid
	}
	r, src, db, e := s.sourceForRead(tx.Statement.Context, tx, p, input.SourceID)
	if e != nil {
		return job, *def.Model, src, nil, e
	}
	fail := func(e error) (GovernanceModelDeployment, GovernanceModel, SourceSecret, *sql.DB, error) {
		db.Close()
		return job, *def.Model, src, nil, e
	}
	if r.Version != input.SourceVersion || r.State == "disabled" || r.State == "archived" {
		return fail(conflict)
	}
	if src.Kind != "postgres" && src.Kind != "mysql" {
		return fail(problem(400, "MODEL_TARGET_UNSUPPORTED", "物理建表支持PostgreSQL和MySQL"))
	}
	if src.Kind == "postgres" {
		var schemas int
		if e = db.QueryRowContext(tx.Statement.Context, "SELECT count(*) FROM pg_namespace WHERE nspname=$1", def.Model.Schema).Scan(&schemas); e != nil {
			return fail(e)
		}
		if schemas != 1 {
			return fail(problem(409, "MODEL_SCHEMA_MISSING", "目标Schema不存在，请由目标库管理员准备后再预检"))
		}
	}
	ddl, e := governanceDDL(def, src.Kind)
	if e != nil {
		return fail(e)
	}
	job.DepartmentID = head.DepartmentID
	job.SourceFingerprint = fingerprint(r.Body)
	job.DDL = ddl
	job.TargetSchema = def.Model.Schema
	job.TargetTable = def.Model.Table
	if src.Kind == "mysql" && job.TargetSchema != src.Database {
		return fail(problem(400, "MODEL_TARGET_SCHEMA", "MySQL模型库名须与数据源数据库一致"))
	}
	return job, *def.Model, src, db, nil
}
func governanceModelPreviewToken(job GovernanceModelDeployment) string {
	return fingerprint(encode([]interface{}{job.ModelID, job.ModelVersion, job.SourceID, job.SourceVersion, job.SourceFingerprint, job.DDL}))
}
func governanceModelTableExists(ctx context.Context, db *sql.DB, kind, schema, table string) (bool, error) {
	q := "SELECT count(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2"
	if kind == "mysql" {
		q = strings.ReplaceAll(strings.ReplaceAll(q, "$1", "?"), "$2", "?")
	}
	var count int
	e := db.QueryRowContext(ctx, q, schema, table).Scan(&count)
	return count > 0, e
}

func (s *Server) registerGovernanceModelDeployments(api, write *gin.RouterGroup) {
	api.GET("/governance/model-deployments", s.require("governance", "governor"), func(c *gin.Context) {
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
		q := workspaceQuery(s.db.WithContext(c.Request.Context()).Model(&GovernanceModelDeployment{}), "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_model_deployments.model_id)")
		if !admin(p) {
			q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		}
		if id := c.Query("model_id"); id != "" {
			q = q.Where("model_id=?", id)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []GovernanceModelDeployment{}
		if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	for _, action := range []string{"deployment-preview", "deployments"} {
		action := action
		write.POST("/governance/models/:id/versions/:version/"+action, s.require("governance", "governor"), func(c *gin.Context) {
			version, e := intPath(c, "version")
			if e != nil {
				s.fail(c, e)
				return
			}
			ref := GovernanceRef{ID: c.Param("id"), Version: version}
			s.mutate("governance_model_"+action, func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
				input, e := bind[governanceModelTarget](b)
				if e != nil {
					return 0, nil, ref.ID, e
				}
				job, _, src, db, e := s.governanceModelBasis(tx, p, ref, input)
				if e != nil {
					return 0, nil, ref.ID, e
				}
				defer db.Close()
				exists, e := governanceModelTableExists(tx.Statement.Context, db, src.Kind, job.TargetSchema, job.TargetTable)
				if e != nil {
					return 0, nil, ref.ID, e
				}
				if action == "deployment-preview" {
					return 200, encode(gin.H{"ddl": job.DDL, "target_exists": exists, "can_execute": !exists, "preview_token": governanceModelPreviewToken(job), "source_version": job.SourceVersion, "verification_scope": "columns_and_primary_key"}), ref.ID, nil
				}
				if exists {
					return 0, nil, ref.ID, problem(409, "MODEL_TARGET_EXISTS", "目标表已经存在；不会覆盖、修改或删除已有表")
				}
				if input.PreviewToken != governanceModelPreviewToken(job) || input.ConfirmTable != job.TargetSchema+"."+job.TargetTable {
					return 0, nil, ref.ID, problem(409, "MODEL_PREVIEW_CHANGED", "请重新预检并确认完整目标表名")
				}
				if e = tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "governance-model:"+job.SourceID+":"+job.TargetSchema+":"+job.TargetTable).Error; e != nil {
					return 0, nil, ref.ID, e
				}
				var active int64
				if e = tx.Model(&GovernanceModelDeployment{}).Where("source_id=? AND target_schema=? AND target_table=? AND state IN ('queued','running','unknown')", job.SourceID, job.TargetSchema, job.TargetTable).Count(&active).Error; e != nil {
					return 0, nil, ref.ID, e
				}
				if active > 0 {
					return 0, nil, ref.ID, conflict
				}
				if e = governanceLock(tx, job.DepartmentID); e != nil {
					return 0, nil, ref.ID, e
				}
				_, versionRow, e := governanceRead(tx, p, "model", ref.ID, ref.Version)
				if e != nil {
					return 0, nil, ref.ID, e
				}
				if versionRow.State != "published" {
					return 0, nil, ref.ID, conflict
				}
				job.ID, e = newID()
				if e != nil {
					return 0, nil, ref.ID, e
				}
				job.State = "queued"
				job.LeaseExpiresAt = time.Unix(0, 0).UTC()
				if e = tx.Create(&job).Error; e != nil {
					return 0, nil, job.ID, e
				}
				return 201, encode(job), job.ID, nil
			})(c)
		})
	}
	write.POST("/governance/model-deployments/:id/reconcile", s.require("governance", "governor"), s.mutatePath("governance_model_reconcile", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, e := bind[struct{}](b); e != nil {
			return 0, nil, id, e
		}
		var job GovernanceModelDeployment
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&job).Error; e != nil {
			return 0, nil, id, e
		}
		if !admin(p) && (job.ActorID != p.UserID || job.DepartmentID != p.DepartmentID) {
			return 0, nil, id, denied
		}
		if job.State != "unknown" {
			return 0, nil, id, conflict
		}
		_, model, src, db, e := s.governanceModelBasis(tx, p, GovernanceRef{ID: job.ModelID, Version: job.ModelVersion}, governanceModelTarget{SourceID: job.SourceID, SourceVersion: job.SourceVersion})
		if e != nil {
			return 0, nil, id, e
		}
		defer db.Close()
		exists, e := governanceModelTableExists(tx.Statement.Context, db, src.Kind, job.TargetSchema, job.TargetTable)
		if e != nil {
			return 0, nil, id, e
		}
		if !exists {
			job.State = "failed"
			job.ErrorCode = "MODEL_NOT_CREATED"
		} else {
			if e = governanceModelVerify(tx.Statement.Context, db, src.Kind, job, model); e != nil {
				return 0, nil, id, e
			}
			job.State = "succeeded"
			job.ErrorCode = ""
		}
		if e = tx.Save(&job).Error; e != nil {
			return 0, nil, id, e
		}
		return 200, encode(job), id, nil
	}))
}

func governanceModelVerify(ctx context.Context, db *sql.DB, kind string, job GovernanceModelDeployment, model GovernanceModel) error {
	q := "SELECT obj_description(c.oid,'pg_class') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2"
	if kind == "mysql" {
		q = "SELECT table_comment FROM information_schema.tables WHERE table_schema=? AND table_name=?"
	}
	var receipt sql.NullString
	if e := db.QueryRowContext(ctx, q, job.TargetSchema, job.TargetTable).Scan(&receipt); e != nil {
		return e
	}
	if receipt.String != "datamesh-model-deployment:"+job.ID {
		return problem(409, "MODEL_RECEIPT_MISMATCH", "目标表缺少本次执行回执，不能认定为本次建表成功")
	}
	actual, e := discoverTable(ctx, db, SourceSecret{Kind: kind, Database: job.TargetSchema}, job.TargetSchema, job.TargetTable)
	if e != nil {
		return e
	}
	expected := governanceModelFields(model)
	if len(actual.Fields) != len(expected) {
		return problem(409, "MODEL_TARGET_MISMATCH", "目标表字段数量与模型不符")
	}
	for i, field := range expected {
		observed := actual.Fields[i]
		logical := observed.LogicalType
		if kind == "mysql" && field.LogicalType == "boolean" && logical == "integer" {
			logical = "boolean"
		}
		if field.Name != observed.Name || field.LogicalType != logical || field.Nullable != observed.Nullable || field.PrimaryKey != observed.PrimaryKey {
			return problem(409, "MODEL_TARGET_MISMATCH", "目标表字段、类型、非空或主键与模型不符")
		}
		if field.LogicalType == "decimal" && (observed.Precision == nil || observed.Scale == nil || *observed.Precision != *field.Precision || *observed.Scale != *field.Scale) {
			return conflict
		}
	}
	for _, field := range model.Fields {
		if field.LogicalType != "string" {
			continue
		}
		q := "SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name=$3"
		if kind == "mysql" {
			q = "SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=? AND table_name=? AND column_name=?"
		}
		var length sql.NullInt64
		if e = db.QueryRowContext(ctx, q, job.TargetSchema, job.TargetTable, field.Name).Scan(&length); e != nil {
			return e
		}
		if field.Length > 0 && (!length.Valid || length.Int64 != int64(field.Length)) {
			return conflict
		}
	}
	return nil
}

func (s *Server) StartGovernanceModelWorker(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if e := s.governanceModelWorkerTick(ctx); e != nil && ctx.Err() == nil {
				slog.Error("governance model worker unavailable")
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func (s *Server) governanceModelWorkerTick(ctx context.Context) error {
	owner, e := newID()
	if e != nil {
		return e
	}
	var job GovernanceModelDeployment
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&GovernanceModelDeployment{}).Where("state='running' AND lease_expires_at<CURRENT_TIMESTAMP").Updates(map[string]interface{}{"state": "unknown", "error_code": "MODEL_COMMIT_UNKNOWN"}).Error; e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='queued'").Order("created_at,id").Take(&job).Error; e != nil {
			return e
		}
		job.State = "running"
		job.LeaseOwner = owner
		job.LeaseExpiresAt = time.Now().Add(90 * time.Second)
		return tx.Save(&job).Error
	})
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state, code := "failed", "MODEL_EXECUTION_FAILED"
	stage := "authorization"
	operation := func() error {
		p, e := s.etlPrincipal(work, job.ActorID)
		if e != nil {
			return e
		}
		if !platform(p, "governance") || !role(p, "governor") {
			return denied
		}
		stage = "basis"
		basis, model, src, db, e := s.governanceModelBasis(s.db.WithContext(work), p, GovernanceRef{ID: job.ModelID, Version: job.ModelVersion}, governanceModelTarget{SourceID: job.SourceID, SourceVersion: job.SourceVersion})
		if e != nil {
			return e
		}
		defer db.Close()
		if basis.SourceFingerprint != job.SourceFingerprint || basis.DDL != job.DDL {
			return conflict
		}
		stage = "target_lookup"
		exists, e := governanceModelTableExists(work, db, src.Kind, job.TargetSchema, job.TargetTable)
		if e != nil {
			return e
		}
		if exists {
			code = "MODEL_TARGET_EXISTS"
			return conflict
		}
		reauthorize := func() error {
			fresh, er := s.etlPrincipal(work, job.ActorID)
			if er != nil {
				return er
			}
			if !platform(fresh, "governance") || !role(fresh, "governor") {
				return denied
			}
			tx := s.db.WithContext(work)
			if er = requireResourceCapability(tx, fresh, Resource{Type: "source", ID: job.SourceID}, "operate_resource"); er != nil {
				return er
			}
			source, er := get(tx, "source", job.SourceID, false)
			if er != nil {
				return er
			}
			if source.Version != job.SourceVersion || fingerprint(source.Body) != job.SourceFingerprint || source.State == "disabled" || source.State == "archived" {
				return conflict
			}
			_, version, er := governanceRead(tx, fresh, "model", job.ModelID, job.ModelVersion)
			if er != nil {
				return er
			}
			if version.State != "published" {
				return conflict
			}
			return nil
		}
		if e = reauthorize(); e != nil {
			return e
		}
		receipt := "datamesh-model-deployment:" + job.ID
		if src.Kind == "postgres" {
			stage = "begin"
			target, e := db.BeginTx(work, nil)
			if e != nil {
				return e
			}
			defer target.Rollback()
			stage = "create"
			if _, e = target.ExecContext(work, job.DDL); e != nil {
				return e
			}
			stage = "receipt"
			comment := "COMMENT ON TABLE " + quoteIdentifier("postgres", job.TargetSchema) + "." + quoteIdentifier("postgres", job.TargetTable) + " IS '" + receipt + "'"
			if _, e = target.ExecContext(work, comment); e != nil {
				return e
			}
			stage = "precommit_authorization"
			if e = reauthorize(); e != nil {
				return e
			}
			stage = "commit"
			if e = target.Commit(); e != nil {
				state = "unknown"
				code = "MODEL_COMMIT_UNKNOWN"
				return e
			}
		} else {
			ddl := strings.TrimSuffix(strings.TrimSpace(job.DDL), ";") + " COMMENT='" + receipt + "'"
			if _, e = db.ExecContext(work, ddl); e != nil {
				state = "unknown"
				code = "MODEL_COMMIT_UNKNOWN"
				return e
			}
		}
		state = "unknown"
		code = "MODEL_COMMIT_UNKNOWN"
		stage = "verify"
		if e = governanceModelVerify(work, db, src.Kind, job, model); e != nil {
			return e
		}
		state = "succeeded"
		code = ""
		return nil
	}
	if err := operation(); err != nil {
		var diagnostic *Problem
		if state == "failed" && errors.As(err, &diagnostic) {
			code = diagnostic.Code
		}
		var sqlCause interface{ SQLState() string }
		sqlState := ""
		if errors.As(err, &sqlCause) {
			sqlState = sqlCause.SQLState()
		}
		slog.Warn("governance model deployment did not complete", "job_id", job.ID, "state", state, "code", code, "stage", stage, "cause_type", fmt.Sprintf("%T", err), "sql_state", sqlState)
	}
	finish, cancelFinish := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinish()
	return s.db.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&GovernanceModelDeployment{}).Where("id=? AND state='running' AND lease_owner=?", job.ID, owner).Updates(map[string]interface{}{"state": state, "error_code": code})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict
		}
		return s.audit(tx, identity.Principal{UserID: job.ActorID, DepartmentID: job.DepartmentID}, "governance_model_finished", job.ID, state, code)
	})
}
