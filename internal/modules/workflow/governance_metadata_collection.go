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
	"gorm.io/gorm/clause"
)

type MetadataCollectionPolicy struct {
	ObjectID        string     `json:"object_id" gorm:"primaryKey"`
	DepartmentID    string     `json:"department_id"`
	ActorID         string     `json:"actor_id"`
	Requested       bool       `json:"requested"`
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	Version         int        `json:"version"`
	NextAt          time.Time  `json:"next_at"`
	LastAttemptAt   *time.Time `json:"last_attempt_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	Failures        int        `json:"failures"`
	ErrorCode       string     `json:"error_code"`
	SourceVersion   int        `json:"source_version"`
	ObjectVersion   int        `json:"object_version"`
	ObservationID   string     `json:"observation_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (MetadataCollectionPolicy) TableName() string { return "metadata_collection_policies" }

type MetadataCollectionRun struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	ObjectID      string    `json:"object_id"`
	ActorID       string    `json:"actor_id"`
	Trigger       string    `json:"trigger"`
	State         string    `json:"state"`
	ObservationID string    `json:"observation_id"`
	Changed       bool      `json:"changed"`
	ErrorCode     string    `json:"error_code"`
	CreatedAt     time.Time `json:"created_at"`
}

func (MetadataCollectionRun) TableName() string { return "metadata_collection_runs" }

type MetadataChangeReview struct {
	ObservationID string     `json:"observation_id" gorm:"primaryKey"`
	ObjectID      string     `json:"object_id"`
	State         string     `json:"state"`
	Note          string     `json:"note"`
	ReviewedBy    *string    `json:"reviewed_by"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (MetadataChangeReview) TableName() string { return "metadata_change_reviews" }

func metadataResourceActive(r Record) bool {
	return !slices.Contains([]string{"disabled", "archived", "retired", "deleted", "suspended"}, r.State)
}
func metadataPolicy(tx *gorm.DB, p identity.Principal, r Record, expectedVersion int) (MetadataCollectionPolicy, error) {
	row := MetadataCollectionPolicy{ObjectID: r.ID, DepartmentID: r.DepartmentID, ActorID: p.UserID, Enabled: true, IntervalMinutes: 1440, Version: 1, NextAt: time.Now().UTC()}
	insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if insert.Error != nil {
		return row, insert.Error
	}
	if insert.RowsAffected == 0 && expectedVersion == 0 {
		return row, conflict
	}
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("object_id=?", r.ID).Take(&row).Error
	if e == nil && insert.RowsAffected == 0 && row.Version != expectedVersion {
		return row, conflict
	}
	return row, e
}
func (s *Server) metadataCollectionDetail(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	id := c.Param("id")
	if _, _, e := metadataTarget(tx, principal(c), id, false); e != nil {
		s.fail(c, e)
		return
	}
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
	changesPage, e := intQuery(c, "changes_page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	changesSize, e := intQuery(c, "changes_page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	var policy MetadataCollectionPolicy
	e = tx.Where("object_id=?", id).Take(&policy).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		s.fail(c, e)
		return
	}
	var result *MetadataCollectionPolicy
	if e == nil {
		result = &policy
	}
	runs := []MetadataCollectionRun{}
	reviews := []MetadataChangeReview{}
	var total, pendingTotal int64
	q := tx.Model(&MetadataCollectionRun{}).Where("object_id=?", id)
	if e = q.Count(&total).Error; e == nil {
		e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&runs).Error
	}
	if e == nil {
		reviewsQuery := tx.Model(&MetadataChangeReview{}).Where("object_id=? AND state IN ('pending','deferred')", id)
		e = reviewsQuery.Count(&pendingTotal).Error
		if e == nil {
			e = reviewsQuery.Order("created_at DESC,observation_id").Offset((changesPage - 1) * changesSize).Limit(changesSize).Find(&reviews).Error
		}
	}
	if e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"policy": result, "items": runs, "total": total, "page": page, "page_size": size, "changes_page_size": changesSize, "pending_reviews": reviews, "pending_total": pendingTotal, "observed_at": time.Now().UTC()})
}
func (s *Server) metadataCollectionSave(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		ExpectedVersion int  `json:"expected_version"`
		Enabled         bool `json:"enabled"`
		IntervalMinutes int  `json:"interval_minutes"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	if input.IntervalMinutes < 15 || input.IntervalMinutes > 43200 {
		return 0, nil, id, invalid
	}
	r, _, e := metadataTarget(tx, p, id, false)
	if e != nil {
		return 0, nil, id, e
	}
	if !metadataResourceActive(r) {
		return 0, nil, id, conflict
	}
	var existing MetadataCollectionPolicy
	e = tx.Where("object_id=?", id).Take(&existing).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, nil, id, e
	}
	if (e == nil && existing.Version != input.ExpectedVersion) || (errors.Is(e, gorm.ErrRecordNotFound) && input.ExpectedVersion != 0) {
		return 0, nil, id, conflict
	}
	row, e := metadataPolicy(tx, p, r, input.ExpectedVersion)
	if e != nil {
		return 0, nil, id, e
	}
	if existing.ObjectID != "" && row.Version != input.ExpectedVersion {
		return 0, nil, id, conflict
	}
	row.ActorID = p.UserID
	row.Enabled = input.Enabled
	row.IntervalMinutes = input.IntervalMinutes
	row.Version++
	row.NextAt = time.Now().UTC()
	row.Requested = false
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}
func (s *Server) metadataCollectionQueue(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		ExpectedVersion int `json:"expected_version"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	r, _, e := metadataTarget(tx, p, id, false)
	if e != nil {
		return 0, nil, id, e
	}
	if !metadataResourceActive(r) {
		return 0, nil, id, conflict
	}
	var old MetadataCollectionPolicy
	e = tx.Where("object_id=?", id).Take(&old).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, nil, id, e
	}
	if old.Version != input.ExpectedVersion {
		return 0, nil, id, conflict
	}
	row, e := metadataPolicy(tx, p, r, input.ExpectedVersion)
	if e != nil {
		return 0, nil, id, e
	}
	if old.ObjectID != "" && row.Version != input.ExpectedVersion {
		return 0, nil, id, conflict
	}
	// A manual request is one-shot; it does not silently enable a paused schedule.
	row.ActorID = p.UserID
	row.NextAt = time.Now().UTC()
	row.Version++
	row.Requested = true
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	return 202, encode(row), id, nil
}
func (s *Server) metadataReview(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		ObservationID string `json:"observation_id"`
		State         string `json:"state"`
		Note          string `json:"note"`
		ImpactChecked bool   `json:"impact_checked"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	if !valid(input.Note, 4000) || !input.ImpactChecked || !slices.Contains([]string{"acknowledged", "deferred"}, input.State) {
		return 0, nil, id, invalid
	}
	if _, _, e = metadataTarget(tx, p, id, false); e != nil {
		return 0, nil, id, e
	}
	var row MetadataChangeReview
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("observation_id=? AND object_id=?", input.ObservationID, id).Take(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if row.State != "pending" && row.State != "deferred" {
		return 0, nil, id, conflict
	}
	// Evaluate dependencies again at confirmation; visibility filtering stays in the existing graph API.
	if _, e = governanceDependencies(tx, id, true); e != nil {
		return 0, nil, id, e
	}
	now := time.Now().UTC()
	row.State = input.State
	row.Note = input.Note
	row.ReviewedBy = &p.UserID
	row.ReviewedAt = &now
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}

// Resources enter the collector through their existing governance assignments, never a privileged synthetic identity.
func (s *Server) bootstrapMetadataCollection(ctx context.Context) error {
	tx := s.db.WithContext(ctx)
	rows := []Record{}
	if e := tx.Where("kind IN ? AND EXISTS (SELECT 1 FROM users u WHERE u.active AND ((u.id=business_records.owner_id AND EXISTS(SELECT 1 FROM user_roles ar WHERE ar.user_id=u.id AND ar.role='admin')) OR (u.department_id=business_records.department_id AND EXISTS(SELECT 1 FROM business_records a WHERE a.kind='assignment' AND a.state='active' AND a.department_id=business_records.department_id AND a.body->>'user_id'=u.id AND a.body->>'capability'='govern_resource' AND (a.body->>'resource_id'='*' OR a.body->>'resource_id'=business_records.id) AND (a.body->>'expires_at')::timestamptz>now()))) AND (EXISTS(SELECT 1 FROM user_roles ur WHERE ur.user_id=u.id AND ur.role='admin') OR (EXISTS(SELECT 1 FROM user_roles ur WHERE ur.user_id=u.id AND ur.role='governor') AND EXISTS(SELECT 1 FROM user_platform_access up WHERE up.user_id=u.id AND up.platform='governance')))) AND state NOT IN ? AND NOT EXISTS (SELECT 1 FROM metadata_collection_policies p WHERE p.object_id=business_records.id)", []string{"object", "dataset", "table_resource", "file_resource", "api_resource"}, []string{"disabled", "archived", "retired", "deleted", "suspended"}).Order("created_at,id").Limit(100).Find(&rows).Error; e != nil {
		return e
	}
	for _, r := range rows {
		candidates := []string{r.OwnerID}
		assigned := []string{}
		if e := tx.Model(&Record{}).Where("kind='assignment' AND state='active' AND department_id=? AND body->>'capability'='govern_resource' AND (body->>'resource_id'='*' OR body->>'resource_id'=?) AND (body->>'expires_at')::timestamptz>now()", r.DepartmentID, r.ID).Distinct().Pluck("body->>'user_id'", &assigned).Error; e != nil {
			return e
		}
		candidates = append(candidates, assigned...)
		for _, actor := range candidates {
			p, e := s.etlPrincipal(ctx, actor)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			if !role(p, "governor") || !platform(p, "governance") {
				continue
			}
			if _, _, e = metadataTarget(tx, p, r.ID, false); e != nil {
				if errors.Is(e, denied) {
					continue
				}
				return e
			}
			row := MetadataCollectionPolicy{ObjectID: r.ID, DepartmentID: r.DepartmentID, ActorID: p.UserID, Enabled: true, IntervalMinutes: 1440, Version: 1, NextAt: time.Now().UTC()}
			if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
				return e
			}
			break
		}
	}
	return nil
}

func (s *Server) sweepMetadataCollection(ctx context.Context) error {
	if e := s.bootstrapMetadataCollection(ctx); e != nil {
		return e
	}
	rows := []MetadataCollectionPolicy{}
	// A source/resource version change schedules collection without waiting for the daily interval.
	q := s.db.WithContext(ctx).Where("(next_at<=now() AND (enabled OR requested)) OR (enabled AND failures=0 AND EXISTS(SELECT 1 FROM business_records r WHERE r.id=metadata_collection_policies.object_id AND r.kind IN ('object','dataset','table_resource','file_resource','api_resource') AND (r.version<>metadata_collection_policies.object_version OR EXISTS(SELECT 1 FROM business_records src WHERE src.kind='source' AND src.id=COALESCE(r.body->>'source_id',r.body->'config'->>'source_id') AND src.version<>metadata_collection_policies.source_version))))")
	if e := q.Order("next_at,object_id").Limit(20).Find(&rows).Error; e != nil {
		return e
	}
	for _, candidate := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row MetadataCollectionPolicy
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("object_id=? AND version=? AND updated_at=?", candidate.ObjectID, candidate.Version, candidate.UpdatedAt).Take(&row).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return nil
				}
				return e
			}
			now := time.Now().UTC()
			trigger := "scheduled"
			if row.LastAttemptAt == nil {
				trigger = "initial"
			} else if row.Requested {
				trigger = "manual"
			}
			p, collectErr := s.etlPrincipal(ctx, row.ActorID)
			var record Record
			var obj MetadataResource
			if collectErr == nil && (!role(p, "governor") || !platform(p, "governance")) {
				collectErr = denied
			}
			if collectErr == nil {
				record, obj, collectErr = metadataTarget(tx, p, row.ObjectID, false)
			}
			if collectErr == nil && !metadataResourceActive(record) {
				collectErr = problem(409, "METADATA_RESOURCE_INACTIVE", "来源已停用，暂停读取")
			}
			sourceVersion := record.Version
			if collectErr == nil && obj.SourceID != "" {
				var source Record
				source, collectErr = get(tx, "source", obj.SourceID, false)
				sourceVersion = source.Version
				if collectErr == nil && !metadataResourceActive(source) {
					collectErr = problem(409, "METADATA_SOURCE_INACTIVE", "数据源已停用")
				}
			}
			if collectErr == nil && row.ObservationID != "" && (record.Version != row.ObjectVersion || sourceVersion != row.SourceVersion) {
				trigger = "resource_changed"
			}
			runID, e := newID()
			if e != nil {
				return e
			}
			run := MetadataCollectionRun{ID: runID, ObjectID: row.ObjectID, ActorID: row.ActorID, Trigger: trigger, State: "succeeded", CreatedAt: now}
			var observation MetadataObservation
			if collectErr == nil {
				// A savepoint retains the run failure while discarding any partial snapshot write.
				collectErr = tx.Transaction(func(inner *gorm.DB) error {
					_, body, _, e := s.metadataObserve(inner, p, row.ObjectID, encode(map[string]int{"source_version": sourceVersion, "object_version": record.Version}))
					if e != nil {
						return e
					}
					current, e := s.etlPrincipal(ctx, row.ActorID)
					if e != nil {
						return e
					}
					if !role(current, "governor") || !platform(current, "governance") {
						return denied
					}
					if _, _, e = metadataTarget(inner, current, row.ObjectID, false); e != nil {
						return e
					}
					return json.Unmarshal(body, &observation)
				})
			}
			row.LastAttemptAt = &now
			row.Requested = false
			if collectErr != nil {
				run.State = "failed"
				run.ErrorCode = captureErrorCode(collectErr)
				if run.ErrorCode == "CAPTURE_UNAVAILABLE" {
					run.ErrorCode = "METADATA_COLLECTION_FAILED"
				}
				var authProblem *Problem
				if errors.Is(collectErr, denied) || errors.Is(collectErr, gorm.ErrRecordNotFound) || (errors.As(collectErr, &authProblem) && authProblem.Status == 403) {
					run.State = "blocked"
					run.ErrorCode = "METADATA_AUTHORIZATION_LOST"
				}
				row.Failures++
				row.ErrorCode = run.ErrorCode
				delay := time.Duration(min(60, 5*(1<<min(row.Failures-1, 4)))) * time.Minute
				row.NextAt = now.Add(delay)
			} else {
				run.ObservationID = observation.ID
				run.Changed = row.ObservationID != observation.ID
				row.ObservationID = observation.ID
				row.ObjectVersion = record.Version
				row.SourceVersion = sourceVersion
				row.LastSuccessAt = &now
				row.Failures = 0
				row.ErrorCode = ""
				row.NextAt = now.Add(time.Duration(row.IntervalMinutes) * time.Minute)
			}
			if !row.Enabled {
				row.NextAt = now
			}
			if e = tx.Save(&row).Error; e != nil {
				return e
			}
			return tx.Create(&run).Error
		})
		if e != nil {
			return e
		}
	}
	return nil
}

// Keep the existing synchronous refresh API in the same maintenance history as scheduled collection.
func (s *Server) metadataRefresh(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	r, _, e := metadataTarget(tx, p, id, false)
	if e != nil {
		return 0, nil, id, e
	}
	var prior MetadataCollectionPolicy
	e = tx.Where("object_id=?", id).Take(&prior).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, nil, id, e
	}
	row, e := metadataPolicy(tx, p, r, prior.Version)
	if e != nil {
		return 0, nil, id, e
	}
	status, body, resource, e := s.metadataObserve(tx, p, id, b)
	if e != nil {
		return status, body, resource, e
	}
	var observed MetadataObservation
	if e = json.Unmarshal(body, &observed); e != nil {
		return 0, nil, id, e
	}
	now := time.Now().UTC()
	runID, e := newID()
	if e != nil {
		return 0, nil, id, e
	}
	run := MetadataCollectionRun{ID: runID, ObjectID: id, ActorID: p.UserID, Trigger: "manual", State: "succeeded", ObservationID: observed.ID, Changed: row.ObservationID != observed.ID, CreatedAt: now}
	row.LastAttemptAt = &now
	row.LastSuccessAt = &now
	row.Failures = 0
	row.ErrorCode = ""
	row.Requested = false
	row.ObservationID = observed.ID
	row.ObjectVersion = observed.ObjectVersion
	row.SourceVersion = observed.SourceVersion
	row.NextAt = now.Add(time.Duration(row.IntervalMinutes) * time.Minute)
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&run).Error; e != nil {
		return 0, nil, id, e
	}
	return status, body, resource, nil
}
