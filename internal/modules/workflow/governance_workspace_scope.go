package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type governanceWorkspaceContextKey struct{}
type governanceWorkspaceScope struct {
	ID       string
	Internal bool
	Mutation bool
}

func workspaceScope(ctx context.Context) governanceWorkspaceScope {
	v, _ := ctx.Value(governanceWorkspaceContextKey{}).(governanceWorkspaceScope)
	return v
}
func workspaceContext(tx *gorm.DB, id string, internal bool) *gorm.DB {
	return tx.WithContext(context.WithValue(tx.Statement.Context, governanceWorkspaceContextKey{}, governanceWorkspaceScope{ID: id, Internal: internal, Mutation: workspaceScope(tx.Statement.Context).Mutation}))
}

// Independent authorization reads must not inherit transaction-local write locks.
func governanceReadContext(ctx context.Context) context.Context {
	scope := workspaceScope(ctx)
	scope.Mutation = false
	return context.WithValue(ctx, governanceWorkspaceContextKey{}, scope)
}
func workspaceID(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func workspacePrivateKind(kind string) bool {
	return kind == "master" || kind == "scenario" || kind == "model" || kind == "dataflow" || kind == "workflow" || kind == "quality_template"
}
func workspaceRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/governance/") || path == "/api/v1/datasets" || strings.HasPrefix(path, "/api/v1/datasets/")
}

// Metadata exploration is scoped to explicitly associated exchange resources.
// Do not compare contract versions here: drift must stay visible for inspection.
func workspaceMetadataSource(tx *gorm.DB, path string) error {
	prefix := "/api/v1/governance/metadata/"
	id := workspaceScope(tx.Statement.Context).ID
	if id == "" || !strings.HasPrefix(path, prefix) {
		return nil
	}
	resourceID := strings.Split(strings.TrimPrefix(path, prefix), "/")[0]
	var count int64
	if e := tx.Model(&GovernanceWorkspaceSource{}).Where("workspace_id=? AND resource_id=? AND resource_type IN ? AND active", id, resourceID, []string{"table_resource", "file_resource", "api_resource"}).Count(&count).Error; e != nil {
		return e
	}
	if count == 0 {
		return problem(409, "GOVERNANCE_WORKSPACE_SOURCE_REQUIRED", "请先在当前空间的来源数据中关联这份共享交换资源，再查看或维护元数据")
	}
	return nil
}
func (s *Server) governanceWorkspaceContext(c *gin.Context) {
	if id := c.Query("workspace_id"); id != "" && workspaceRoute(c.Request.URL.Path) {
		if !valid(id, 128) || strings.TrimSpace(id) != id {
			s.fail(c, invalid)
			c.Abort()
			return
		}
		if _, e := governanceWorkspaceRead(s.db.WithContext(c.Request.Context()), principal(c), id, false); e != nil {
			s.fail(c, e)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), governanceWorkspaceContextKey{}, governanceWorkspaceScope{ID: id}))
	}
	if e := workspaceMetadataSource(s.db.WithContext(c.Request.Context()), c.Request.URL.Path); e != nil {
		s.fail(c, e)
		c.Abort()
		return
	}
	c.Next()
}

// The envelope stays in the idempotency hash; business payloads retain strict decoding.
func governanceWorkspaceEnvelope(tx *gorm.DB, path string, body []byte) (*gorm.DB, []byte, error) {
	if !workspaceRoute(path) {
		return tx, body, nil
	}
	if e := tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return tx, nil, e
	}
	scope := workspaceScope(tx.Statement.Context)
	scope.Mutation = true
	tx = tx.WithContext(context.WithValue(tx.Statement.Context, governanceWorkspaceContextKey{}, scope))
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return tx, body, nil
	}
	raw, ok := object["workspace_id"]
	if !ok {
		return tx, body, workspaceMetadataSource(tx, path)
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || !valid(id, 128) || strings.TrimSpace(id) != id {
		return tx, nil, invalid
	}
	if scope := workspaceScope(tx.Statement.Context); scope.ID != "" && scope.ID != id {
		return tx, nil, denied
	}
	delete(object, "workspace_id")
	clean, err := json.Marshal(object)
	if err != nil {
		return tx, nil, err
	}
	tx = workspaceContext(tx, id, false)
	return tx, clean, workspaceMetadataSource(tx, path)
}
func governanceLegacyWorkspace(tx *gorm.DB, department string) (GovernanceWorkspace, error) {
	var id string
	if e := tx.Raw("SELECT governance_legacy_workspace(?)", department).Scan(&id).Error; e != nil {
		return GovernanceWorkspace{}, e
	}
	var result GovernanceWorkspace
	e := tx.Where("id=?", id).Take(&result).Error
	return result, e
}
func governanceWorkspaceRead(tx *gorm.DB, p identity.Principal, id string, forWrite bool) (GovernanceWorkspace, error) {
	var row GovernanceWorkspace
	q := tx.Where("id=?", id)
	if forWrite {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&row).Error; e != nil {
		return row, e
	}
	if !admin(p) && row.DepartmentID != p.DepartmentID {
		return row, denied
	}
	if forWrite && row.State != "active" {
		return row, problem(409, "GOVERNANCE_WORKSPACE_ARCHIVED", "此治理空间已归档，请进入活动空间继续处理；历史结果仍可查看")
	}
	return row, nil
}
func governanceWorkspaceLock(tx *gorm.DB, id string) (GovernanceWorkspace, error) {
	if e := tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return GovernanceWorkspace{}, e
	}
	var row GovernanceWorkspace
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	return row, e
}
func governanceWorkspaceMatch(ctx context.Context, actual string) error {
	if v := workspaceScope(ctx); v.ID != "" && actual != "" && actual != v.ID {
		return problem(403, "GOVERNANCE_WORKSPACE_MISMATCH", "当前对象属于另一个治理空间，请返回它所属的空间操作")
	}
	return nil
}
func governanceWorkspaceWrite(tx *gorm.DB, p identity.Principal, id string) (GovernanceWorkspace, error) {
	if e := governanceWorkspaceMatch(tx.Statement.Context, id); e != nil {
		return GovernanceWorkspace{}, e
	}
	row, e := governanceWorkspaceLock(tx, id)
	if e != nil {
		return row, e
	}
	if !admin(p) && row.DepartmentID != p.DepartmentID {
		return row, denied
	}
	scope := workspaceScope(tx.Statement.Context)
	if scope.ID == "" && !scope.Internal && !row.IsLegacy {
		return row, problem(409, "GOVERNANCE_WORKSPACE_REQUIRED", "请进入这项任务所属的治理空间后再操作")
	}
	if row.State != "active" {
		return row, problem(409, "GOVERNANCE_WORKSPACE_ARCHIVED", "此治理空间已归档，不能再修改或启动任务；历史成果仍可查看")
	}
	return row, nil
}
func governanceWorkspaceCreateScope(tx *gorm.DB, p identity.Principal, department string) (GovernanceWorkspace, error) {
	id := workspaceScope(tx.Statement.Context).ID
	if id == "" {
		legacy, e := governanceLegacyWorkspace(tx, department)
		if e != nil {
			return legacy, e
		}
		id = legacy.ID
	}
	row, e := governanceWorkspaceWrite(tx, p, id)
	if e == nil && row.DepartmentID != department {
		return row, denied
	}
	return row, e
}
func (s *Server) governanceWorkspaceFilter(c *gin.Context, q *gorm.DB, column string, legacy bool) (*gorm.DB, error) {
	id := workspaceScope(c.Request.Context()).ID
	if id == "" && !legacy {
		return q, nil
	}
	if id == "" {
		row, e := governanceLegacyWorkspace(s.db.WithContext(c.Request.Context()), principal(c).DepartmentID)
		if e != nil {
			return q, e
		}
		id = row.ID
	}
	if _, e := governanceWorkspaceRead(s.db.WithContext(c.Request.Context()), principal(c), id, false); e != nil {
		return q, e
	}
	return q.Where(column+"=?", id), nil
}

func workspaceQuery(q *gorm.DB, column string) *gorm.DB {
	if id := workspaceScope(q.Statement.Context).ID; id != "" {
		return q.Where(column+"=?", id)
	}
	return q
}

const workspaceDatasetColumn = "COALESCE((SELECT r.workspace_id FROM governance_runs r JOIN governance_outputs o ON o.run_id=r.id WHERE o.dataset_id=business_records.id),(SELECT l.workspace_id FROM governance_workspace_legacy_datasets l WHERE l.dataset_id=business_records.id))"

func governanceDatasetWorkspace(tx *gorm.DB, id string) (string, error) {
	var value string
	e := tx.Table("business_records").Where("kind='dataset' AND id=?", id).Select(workspaceDatasetColumn).Scan(&value).Error
	if e == nil && value == "" {
		e = gorm.ErrRecordNotFound
	}
	return value, e
}

// Asset publication keeps its original provider authorization and can originate outside governance.
func governanceDatasetWorkspaceWrite(tx *gorm.DB, p identity.Principal, id string) error {
	actual, e := governanceDatasetWorkspace(tx, id)
	if e != nil {
		return e
	}
	if e = governanceWorkspaceMatch(tx.Statement.Context, actual); e != nil {
		return e
	}
	// The provider keeps publication duties when another department processed its data.
	if !admin(p) {
		record, err := get(tx, "dataset", id, false)
		if err != nil {
			return err
		}
		data, err := decode[DatasetData](record.Body)
		if err != nil {
			return err
		}
		if record.DepartmentID == p.DepartmentID && data.Governance != nil && data.Governance.SingleSource && data.Governance.ProviderDepartmentID == record.DepartmentID {
			space, err := governanceWorkspaceLock(tx, actual)
			if err != nil {
				return err
			}
			if space.State != "active" {
				return problem(409, "GOVERNANCE_WORKSPACE_ARCHIVED", "此治理空间已归档，请由治理人员在活动空间重新处理")
			}
			return nil
		}
	}
	_, e = governanceWorkspaceRead(tx, p, actual, true)
	return e
}

func governanceDatasetWorkspaceRead(tx *gorm.DB, id string) error {
	if workspaceScope(tx.Statement.Context).ID == "" {
		return nil
	}
	actual, e := governanceDatasetWorkspace(tx, id)
	if e != nil {
		return e
	}
	return governanceWorkspaceMatch(tx.Statement.Context, actual)
}
