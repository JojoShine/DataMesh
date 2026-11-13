package workflow

import (
	"datamesh/internal/modules/identity"
	"testing"
	"time"
)

func TestAssetProductValidation(t *testing.T) {
	v := AssetProductDefinition{Name: "产品", Description: "业务用途", Resources: []Resource{{Type: "raw_object", ID: "source", Version: 1}}, Terms: []GovernanceRef{}}
	if validateAssetProduct(v) != nil {
		t.Fatal("valid definition rejected")
	}
	for _, mutate := range []func(*AssetProductDefinition){func(d *AssetProductDefinition) { d.Resources = nil }, func(d *AssetProductDefinition) { d.Resources = append(d.Resources, d.Resources[0]) }, func(d *AssetProductDefinition) { d.Contract.FreshnessSeconds = -1 }, func(d *AssetProductDefinition) { v := 1.01; d.Contract.MaxFailureRate = &v }, func(d *AssetProductDefinition) { d.Domain = &GovernanceRef{ID: "domain", Version: 0} }} {
		copy := v
		copy.Resources = append([]Resource{}, v.Resources...)
		mutate(&copy)
		if validateAssetProduct(copy) == nil {
			t.Fatal("invalid input accepted", copy)
		}
	}
}
func TestAssetContractEvidence(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	future := now.Add(time.Hour)
	rows, rejected := int64(90), int64(10)
	rate := 0.05
	member := AssetProductMember{Fields: []assetField{{Name: "id", LogicalType: "integer"}}}
	valid := assetEvidence{Fields: member.Fields, GeneratedAt: &now, OutputRows: &rows, RejectedRows: &rejected}
	contract := AssetProductContract{FreshnessSeconds: 3600, MaxFailureRate: &rate}
	for _, test := range []struct {
		name     string
		evidence *assetEvidence
		want     string
	}{
		{"sharing unavailable", nil, "violated"},
		{"missing metrics", &assetEvidence{Fields: member.Fields}, "unknown"},
		{"schema changed", &assetEvidence{Fields: []assetField{{Name: "id", LogicalType: "string"}}}, "violated"},
		{"expired snapshot", &assetEvidence{Fields: member.Fields, GeneratedAt: &old}, "violated"},
		{"future timestamp", &assetEvidence{Fields: member.Fields, GeneratedAt: &future}, "unknown"},
		{"isolation exceeds limit", &valid, "violated"},
		{"api source unavailable", &assetEvidence{Fields: member.Fields, Notice: "来源不可交付"}, "violated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := assetCheckState(assetContractEvaluate(member, contract, test.evidence, now)); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
	rejected = 0
	if got := assetCheckState(assetContractEvaluate(member, contract, &valid, now)); got != "passed" {
		t.Fatal(got)
	}
	// Empty runtime output is unmeasured, not zero failures at a known rate.
	rows = 0
	if got := assetCheckState(assetContractEvaluate(member, contract, &valid, now)); got != "unknown" {
		t.Fatal(got)
	}
}
func TestAssetProductSeparationOfDuties(t *testing.T) {
	v := AssetProduct{OwnerID: "author", DepartmentID: "dept"}
	for _, p := range []identity.Principal{{UserID: "author", DepartmentID: "dept", Roles: []string{"admin"}}, {UserID: "outsider", DepartmentID: "else", Roles: []string{"governor"}}, {UserID: "reader", DepartmentID: "dept", Roles: []string{"consumer"}}} {
		if assetProductReview(p, v) {
			t.Fatal("unauthorized review", p)
		}
	}
	if !assetProductReview(identity.Principal{UserID: "reviewer", DepartmentID: "dept", Roles: []string{"governor"}}, v) {
		t.Fatal("reviewer denied")
	}
	if assetProductManage(identity.Principal{UserID: "peer", DepartmentID: "dept", Roles: []string{"provider"}}, v) {
		t.Fatal("peer can edit")
	}
}

func TestRetiredProductDoesNotOfferReview(t *testing.T) {
	reviewer := identity.Principal{UserID: "reviewer", DepartmentID: "dept", Roles: []string{"governor"}}
	product := AssetProduct{ID: "product", OwnerID: "author", DepartmentID: "dept", State: "retired"}
	revision := AssetProductRevision{ID: "revision", State: "submitted"}
	result, e := decode[map[string]interface{}](assetProductView(reviewer, product, revision))
	if e != nil {
		t.Fatal(e)
	}
	if result["can_review"] != false {
		t.Fatal("retired product offers an approval action")
	}
}
