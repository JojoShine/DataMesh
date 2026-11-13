package workflow

import (
	"strings"
	"testing"
)

func TestGovernanceModelContracts(t *testing.T) {
	definition := GovernanceDefinition{Name: "客户", Description: "客户模型", OwnerID: "owner", StewardID: "steward", Model: &GovernanceModel{Layer: "physical", Schema: "public", Table: "customers", Fields: []GovernanceField{{Name: "customer_id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "decimal", Precision: 18, Scale: 2}}, Contract: GovernanceContract{FreshnessSeconds: 3600}}}
	if err := validateGovernanceDefinition("model", definition); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*GovernanceDefinition)
	}{
		{"nullable key", func(v *GovernanceDefinition) { v.Model.Fields[0].Nullable = true }},
		{"duplicate field", func(v *GovernanceDefinition) { v.Model.Fields[1].Name = "customer_id" }},
		{"invalid precision", func(v *GovernanceDefinition) { v.Model.Fields[1].Scale = 19 }},
		{"no key", func(v *GovernanceDefinition) { v.Model.Fields[0].PrimaryKey = false }},
		{"injected identifier", func(v *GovernanceDefinition) { v.Model.Table = "customers;DROP TABLE users" }},
		{"unknown layer", func(v *GovernanceDefinition) { v.Model.Layer = "anything" }},
		{"invalid quality", func(v *GovernanceDefinition) { v.Model.Contract.MaxFailureRate = 1.1 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			v, err := decode[GovernanceDefinition](encode(definition))
			if err != nil {
				t.Fatal(err)
			}
			change.apply(&v)
			if validateGovernanceDefinition("model", v) == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
	for _, dialect := range []string{"postgres", "mysql"} {
		ddl, err := governanceDDL(definition, dialect)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "PRIMARY KEY") || !strings.Contains(ddl, "DECIMAL(18,2)") {
			t.Fatal(ddl)
		}
	}
	definition.Model.Table = "order"
	ddl, err := governanceDDL(definition, "postgres")
	if err != nil || !strings.Contains(ddl, `"order"`) {
		t.Fatalf("reserved identifier: %s %v", ddl, err)
	}
	if _, err := governanceDDL(definition, "oracle"); err == nil {
		t.Fatal("unsupported dialect")
	}
}

func TestGovernanceContractChanges(t *testing.T) {
	base := GovernanceDefinition{Name: "客户", OwnerID: "owner", StewardID: "owner", Model: &GovernanceModel{Layer: "logical", Fields: []GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true, Sensitive: true}}}}
	next, err := decode[GovernanceDefinition](encode(base))
	if err != nil {
		t.Fatal(err)
	}
	next.Model.Fields[1].Nullable = false
	next.Model.Fields[1].Sensitive = false
	changes := governanceChanges("model", base, next)
	if len(changes) != 2 || !changes[0].Breaking || !changes[1].Breaking {
		t.Fatalf("missing breaking changes: %+v", changes)
	}
	next.Model.Fields = append(next.Model.Fields, GovernanceField{Name: "optional", LogicalType: "string", Nullable: true})
	changes = governanceChanges("model", base, next)
	if len(changes) != 3 || changes[2].Breaking {
		t.Fatalf("optional addition: %+v", changes)
	}
	next.Model.Fields = next.Model.Fields[:1]
	changes = governanceChanges("model", base, next)
	if len(changes) != 1 || changes[0].Code != "field_removed" || !changes[0].Breaking {
		t.Fatalf("removal: %+v", changes)
	}
}

func TestGovernanceStandards(t *testing.T) {
	definition := GovernanceDefinition{Name: "状态", OwnerID: "owner", StewardID: "owner", Standard: &GovernanceStandard{Code: "customer.status", LogicalType: "string", Length: 16, Values: []string{"active", "inactive"}, Pattern: "^[a-z]+$"}}
	if validateGovernanceDefinition("standard", definition) != nil {
		t.Fatal("valid standard")
	}
	definition.Standard.Values = []string{"active", "active"}
	if validateGovernanceDefinition("standard", definition) == nil {
		t.Fatal("duplicate codes accepted")
	}
	definition.Standard.Values = []string{"active"}
	definition.Standard.Pattern = "["
	if validateGovernanceDefinition("standard", definition) == nil {
		t.Fatal("bad regex accepted")
	}
	definition.Standard = nil
	if validateGovernanceDefinition("standard", definition) == nil {
		t.Fatal("missing standard accepted")
	}
	if validateGovernanceDefinition("domain", GovernanceDefinition{Name: "主题域", OwnerID: "owner", StewardID: "owner", Model: &GovernanceModel{Layer: "conceptual"}}) == nil {
		t.Fatal("wrong payload accepted")
	}
}

func TestGovernanceStandardTypedValues(t *testing.T) {
	for _, tc := range []struct {
		typ              string
		precision, scale int
		good, bad        string
	}{
		{"decimal", 5, 2, "123.45", "1234.56"},
		{"date", 0, 0, "2026-10-21", "2026-02-31"},
		{"datetime", 0, 0, "2026-10-21T12:34:56Z", "tomorrow"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			definition := GovernanceDefinition{Name: "标准", OwnerID: "owner", StewardID: "owner", Standard: &GovernanceStandard{Code: "business.code", LogicalType: tc.typ, Precision: tc.precision, Scale: tc.scale, Values: []string{tc.good}}}
			if err := validateGovernanceDefinition("standard", definition); err != nil {
				t.Fatalf("good value rejected: %v", err)
			}
			definition.Standard.Values = []string{tc.bad}
			if err := validateGovernanceDefinition("standard", definition); err == nil {
				t.Fatal("invalid typed code accepted")
			}
		})
	}
}

func TestGovernanceCompositeKeysAndRelationships(t *testing.T) {
	definition := GovernanceDefinition{Name: "租户客户", OwnerID: "owner", StewardID: "owner", Model: &GovernanceModel{Layer: "physical", Schema: "public", Table: "tenant_customers", Fields: []GovernanceField{{Name: "tenant_id", LogicalType: "integer", PrimaryKey: true}, {Name: "customer_id", LogicalType: "integer", PrimaryKey: true}}, Relations: []GovernanceRelation{{Name: "customer_tenant", Target: GovernanceRef{ID: "tenant-model", Version: 1}, Fields: []string{"tenant_id"}, TargetFields: []string{"id"}, Cardinality: "many_to_one"}}}}
	if err := validateGovernanceDefinition("model", definition); err != nil {
		t.Fatal(err)
	}
	ddl, err := governanceDDL(definition, "postgres")
	if err != nil || !strings.Contains(ddl, `PRIMARY KEY ("tenant_id", "customer_id")`) {
		t.Fatalf("composite key lost: %s %v", ddl, err)
	}
	definition.Model.Relations[0].TargetFields = nil
	if validateGovernanceDefinition("model", definition) == nil {
		t.Fatal("unmatched relation fields accepted")
	}
	definition.Model.Relations[0].TargetFields = []string{"id"}
	definition.Model.Relations[0].Target.Version = 0
	if validateGovernanceDefinition("model", definition) == nil {
		t.Fatal("unpinned relationship accepted")
	}
}
