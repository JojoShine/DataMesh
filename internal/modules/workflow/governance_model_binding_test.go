package workflow

import "testing"

func TestGovernanceModelBindingDraft(t *testing.T) {
	latest := GovernanceDefinition{Name: "latest", Dataflow: &GovernanceDataflow{Nodes: []GovernanceFlowNode{{ID: "source", Kind: "source"}, {ID: "output", Kind: "output", InferModel: true, DataLayer: "dwd"}}}}
	model := GovernanceModel{Layer: "logical", DataLayer: "dws"}
	ref := GovernanceRef{ID: "model", Version: 2}
	draft, err := governanceModelBindingDefinition(latest, "output", ref, model)
	if err != nil {
		t.Fatal(err)
	}
	output := draft.Dataflow.Nodes[1]
	if output.InferModel || output.Model == nil || *output.Model != ref || output.DataLayer != "" {
		t.Fatal("model output must inherit the model layer")
	}
	if !latest.Dataflow.Nodes[1].InferModel || latest.Dataflow.Nodes[1].Model != nil {
		t.Fatal("changed original definition")
	}
	if draft.Name != latest.Name || draft.Dataflow.Nodes[0].ID != "source" {
		t.Fatal("lost latest flow content")
	}
	model.DataLayer = ""
	draft, err = governanceModelBindingDefinition(latest, "output", ref, model)
	if err != nil || draft.Dataflow.Nodes[1].DataLayer != "dwd" {
		t.Fatal("lost legacy output layer fallback")
	}
	if _, err = governanceModelBindingDefinition(latest, "removed", ref, model); err == nil {
		t.Fatal("accepted missing source output")
	}
	model.Layer = "conceptual"
	if _, err = governanceModelBindingDefinition(latest, "output", ref, model); err == nil {
		t.Fatal("accepted conceptual output")
	}
}
