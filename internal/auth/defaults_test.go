package auth

import "testing"

func TestProductionAuthMissingOnlyBlocksUnsafeMethods(t *testing.T) {
	if !ProductionAuthMissing(true, "", "POST") {
		t.Fatal("expected missing production auth to block POST")
	}
	if ProductionAuthMissing(true, "", "GET") {
		t.Fatal("did not expect missing production auth to block GET")
	}
	if ProductionAuthMissing(true, "token", "POST") {
		t.Fatal("did not expect configured bearer token to block POST")
	}
	if ProductionAuthMissing(false, "", "POST") {
		t.Fatal("did not expect dev auth policy to block POST")
	}
}
