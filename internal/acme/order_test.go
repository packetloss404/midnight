package acme

import "testing"

func TestOrderStateValidateAcceptsValidOrder(t *testing.T) {
	state := OrderState{
		Domain:            " App.Example.Test ",
		Status:            OrderStatusValid,
		AuthorizationURLs: []string{" ", "https://ca.test/authz/1 "},
		CertificateURL:    " https://ca.test/cert/1 ",
	}

	if err := state.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	normalized := state.Normalized()
	if normalized.Domain != "app.example.test" || normalized.CertificateURL != "https://ca.test/cert/1" {
		t.Fatalf("unexpected normalized order: %#v", normalized)
	}
	if len(normalized.AuthorizationURLs) != 1 || normalized.AuthorizationURLs[0] != "https://ca.test/authz/1" {
		t.Fatalf("authorization URLs were not compacted: %#v", normalized.AuthorizationURLs)
	}
}

func TestOrderStateValidateRejectsInvalidState(t *testing.T) {
	tests := map[string]OrderState{
		"missing domain":         {Status: OrderStatusPending},
		"unknown status":         {Domain: "example.test", Status: "done"},
		"valid without cert URL": {Domain: "example.test", Status: OrderStatusValid},
		"invalid without error":  {Domain: "example.test", Status: OrderStatusInvalid},
	}

	for name, state := range tests {
		if err := state.Validate(); err == nil {
			t.Fatalf("%s: Validate returned nil", name)
		}
	}
}
