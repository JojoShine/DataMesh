package workflow

import "testing"

func TestMountInputRejectsGovernanceVariantClaim(t *testing.T) {
	_, err := bind[MountInput]([]byte(`{"directory_id":"original","resource":{"resource_type":"raw_object","resource_id":"source","contract_version":1},"mapping":{"id":"id"},"governance_variant":true}`))
	if err == nil {
		t.Fatal("caller can claim a trusted governance variant")
	}
}

func TestGovernanceVariantRequiresImmutableOriginalCatalogProof(t *testing.T) {
	source := Resource{Type: "table_resource", ID: "source", Version: 1}
	data := DatasetData{GovernanceRunID: "run", Governance: &GovernanceProvenance{SingleSource: true, Source: &source, ProviderDepartmentID: "provider", OriginalDirectoryID: "original", Steps: []GovernanceTreatment{{Kind: "quality", Name: "检查必填字段", Description: "客户姓名不能为空"}}}}
	record := Record{ID: "run", DepartmentID: "provider", OwnerID: "governor", Version: 1}
	run := GovernanceRun{ID: "run", Kind: "dataflow", State: "succeeded", ActorID: "governor", DepartmentID: "execution"}
	if !governanceVariantMatches("original", record, data, data, run) {
		t.Fatal("verified single-source output cannot coexist with its original resource")
	}
	for _, scenario := range []string{"other_directory", "multiple_sources", "missing_source", "wrong_department", "changed_steps", "failed_run", "different_owner", "changed_contract", "legacy_dataset"} {
		t.Run(scenario, func(t *testing.T) {
			candidate, err := decode[DatasetData](encode(data))
			if err != nil {
				t.Fatal(err)
			}
			directory, current, execution := "original", record, run
			switch scenario {
			case "other_directory":
				directory = "unrelated"
			case "multiple_sources":
				candidate.Governance.SingleSource = false
			case "missing_source":
				candidate.Governance.Source = nil
			case "wrong_department":
				current.DepartmentID = "execution"
			case "changed_steps":
				candidate.Governance.Steps[0].Description = "伪造治理说明"
			case "failed_run":
				execution.State = "failed"
			case "different_owner":
				current.OwnerID = "other-user"
			case "changed_contract":
				current.Version = 2
			case "legacy_dataset":
				candidate.GovernanceRunID = ""
			}
			if governanceVariantMatches(directory, current, candidate, data, execution) {
				t.Fatal("unproven catalog variant accepted")
			}
		})
	}
}

func TestMountInputRejectsServiceVariantClaim(t *testing.T) {
	if _, err := bind[MountInput]([]byte(`{"directory_id":"directory","resource":{"resource_type":"api_resource","resource_id":"api","contract_version":1},"mapping":{"id":"id"},"service_variant":true}`)); err == nil {
		t.Fatal("caller cannot claim service presentation status")
	}
}
