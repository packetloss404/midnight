package configcode

import "testing"

func TestValidateRejectsUnknownProjectSlug(t *testing.T) {
	doc := Document{APIVersion: APIVersion, Services: []Service{{ProjectSlug: "missing", Slug: "web"}}}
	if err := Validate(doc); err == nil {
		t.Fatal("Validate() error = nil, want invalid document")
	}
}

func TestDiffClassifiesCreateUpdateAndNoop(t *testing.T) {
	doc := Document{
		APIVersion: APIVersion,
		Projects: []Project{
			{Slug: "alpha", DisplayName: "Alpha"},
			{Slug: "beta", DisplayName: "Beta"},
		},
		Services: []Service{{ProjectSlug: "alpha", Slug: "web", DisplayName: "Web"}},
	}
	state := State{
		Projects: []Project{
			{Slug: "alpha", DisplayName: "Old Alpha"},
			{Slug: "beta", DisplayName: "Beta"},
		},
	}

	plan, err := Diff(doc, state)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Create != 1 || plan.Summary.Update != 1 || plan.Summary.Noop != 1 || plan.Summary.Total != 3 {
		t.Fatalf("summary = %#v", plan.Summary)
	}
	if plan.Actions[0].ResourceType != "project" || plan.Actions[0].Slug != "alpha" || plan.Actions[0].Operation != OperationUpdate {
		t.Fatalf("first action = %#v", plan.Actions[0])
	}
	if plan.Actions[2].ResourceType != "service" || plan.Actions[2].Operation != OperationCreate {
		t.Fatalf("third action = %#v", plan.Actions[2])
	}
}
