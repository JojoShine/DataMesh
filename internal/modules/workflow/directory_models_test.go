package workflow

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http/httptest"
	"testing"
)

func TestWorkflowDatabaseConflictIs409(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	s := &Server{}
	s.fail(c, gorm.ErrDuplicatedKey)
	if w.Code != 409 {
		t.Fatalf("translated unique conflict=%d", w.Code)
	}
}

func TestDirectoryDefinitionValidation(t *testing.T) {
	v := DirectoryDefinition{Name: "客户信息", Description: "测试目录", Classification: "department", ClassificationPath: "department/test", ResourceCode: "TEST-001", DictionaryID: "test", ProviderName: "测试部门", ProviderCode: "TEST", ResponsibleUserID: "u", UpdateFrequency: "每日", Format: "database", DataLayer: "raw_object", Items: []DirectoryItem{{Name: "id", BusinessName: "编号", LogicalType: "integer"}}, SharingType: "conditional", SharingConditions: "依法履职", SharingMethod: "query", UsageRestrictions: "限批准用途", SecurityClassification: "一般数据", DepartmentApprovalRef: "test-approval", AssessmentRef: "test-assessment"}
	if e := validateDirectory(v, true); e != nil {
		t.Fatal(e)
	}
	t.Run("conditional_requires_conditions", func(t *testing.T) {
		bad := v
		bad.SharingConditions = ""
		if validateDirectory(bad, true) == nil {
			t.Fatal("missing conditions accepted")
		}
	})
	t.Run("not_shared_requires_legal_basis", func(t *testing.T) {
		bad := v
		bad.SharingType = "not_shared"
		bad.NonSharingReason = "测试"
		if validateDirectory(bad, true) == nil {
			t.Fatal("missing legal basis accepted")
		}
	})
	t.Run("duplicate_items", func(t *testing.T) {
		bad := v
		bad.Items = append(append([]DirectoryItem{}, v.Items...), v.Items[0])
		if validateDirectory(bad, false) == nil {
			t.Fatal("duplicate accepted")
		}
	})
	t.Run("invalid_precision", func(t *testing.T) {
		bad := v
		bad.Items = append([]DirectoryItem{}, v.Items...)
		n := -1
		bad.Items[0].Scale = &n
		if validateDirectory(bad, false) == nil {
			t.Fatal("negative scale accepted")
		}
	})
}
