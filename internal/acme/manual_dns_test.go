package acme

import "testing"

func TestGenerateManualDNSInstruction(t *testing.T) {
	instruction, err := GenerateManualDNSInstruction(" _acme-challenge.example.test. ", " token-value ")
	if err != nil {
		t.Fatalf("GenerateManualDNSInstruction returned error: %v", err)
	}

	if instruction.RecordName != "_acme-challenge.example.test." {
		t.Fatalf("RecordName = %q", instruction.RecordName)
	}
	if instruction.RecordType != "TXT" {
		t.Fatalf("RecordType = %q", instruction.RecordType)
	}
	if instruction.Value != "token-value" {
		t.Fatalf("Value = %q", instruction.Value)
	}
	if instruction.TTLSeconds != 300 {
		t.Fatalf("TTLSeconds = %d", instruction.TTLSeconds)
	}
	if instruction.Action != "create-or-update" {
		t.Fatalf("Action = %q", instruction.Action)
	}
}

func TestGenerateManualDNSInstructionRejectsMissingInputs(t *testing.T) {
	if _, err := GenerateManualDNSInstruction("", "token"); err == nil {
		t.Fatal("expected missing record name error")
	}
	if _, err := GenerateManualDNSInstruction("_acme-challenge.example.test.", ""); err == nil {
		t.Fatal("expected missing value error")
	}
}
