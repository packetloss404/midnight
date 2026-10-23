package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/test/drills/harness"
)

func TestBuildJUnitDocumentGroupsByDrillAndIncludesTimings(t *testing.T) {
	report := gateReport{
		DrillExecution: &drillExecutionSummary{
			Adapter:    "local",
			Assertions: 4,
			Passed:     2,
			Failed:     1,
			Skipped:    1,
			Results: []harness.AssertionResult{
				{ID: "D-01.headline", Status: harness.AssertionPassed, Diagnostic: "ok"},
				{ID: "D-01.envelope.reason-code", Status: harness.AssertionPassed},
				{ID: "D-02.headline", Status: harness.AssertionFailed, ReasonCode: "phase0.alert.disk-pressure", Diagnostic: "expected fail"},
				{ID: "D-03.headline", Status: harness.AssertionSkipped, ReasonCode: "phase0.skipped"},
			},
		},
	}
	doc := buildJUnitDocument(report)
	if doc.Tests != 4 || doc.Failures != 1 || doc.Skipped != 1 {
		t.Fatalf("doc top-level counts = %+v, want 4/1/1", doc)
	}
	if len(doc.Suites) != 3 {
		t.Fatalf("Suites = %d, want 3 (one per drill)", len(doc.Suites))
	}
	for _, suite := range doc.Suites {
		if suite.Time == "" {
			t.Fatalf("suite %s time empty", suite.Name)
		}
		for _, c := range suite.Cases {
			if c.Time == "" {
				t.Fatalf("case %s time empty", c.Name)
			}
		}
	}
	if err := validateJUnitDocument(doc); err != nil {
		t.Fatalf("validateJUnitDocument() error = %v", err)
	}
	xmlBytes, err := renderJUnitXML(doc)
	if err != nil {
		t.Fatalf("renderJUnitXML() error = %v", err)
	}
	got := string(xmlBytes)
	for _, want := range []string{`tests="4"`, `failures="1"`, `skipped="1"`, `<failure`, `<skipped`, `time=`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered XML missing %q:\n%s", want, got)
		}
	}
	// Round-trip-decode to ensure schema-shape survives.
	var decoded junitDocument
	if err := xml.Unmarshal(xmlBytes, &decoded); err != nil {
		t.Fatalf("xml.Unmarshal() error = %v", err)
	}
	if decoded.Tests != doc.Tests {
		t.Fatalf("decoded.Tests = %d, want %d", decoded.Tests, doc.Tests)
	}
}

func TestValidateJUnitDocumentRejectsInconsistentCounts(t *testing.T) {
	doc := junitDocument{Name: "phase0-gate", Tests: 1, Failures: 1, Suites: []junitSuiteDoc{{Name: "D-01", Tests: 1, Failures: 0, Cases: []junitCaseDoc{{Name: "x", Time: "0.001"}}}}}
	if err := validateJUnitDocument(doc); err == nil {
		t.Fatal("validateJUnitDocument() error = nil, want count mismatch")
	}
}

func TestWriteJUnitReportFromReportRequiresXMLPath(t *testing.T) {
	if err := writeJUnitReportFromReport(filepath.Join(t.TempDir(), "report.txt"), gateReport{}); err == nil || !strings.Contains(err.Error(), ".xml") {
		t.Fatalf("writeJUnitReportFromReport() error = %v, want .xml requirement", err)
	}
}

func TestWriteJUnitReportFromReportWritesPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "phase0.xml")
	report := gateReport{
		DrillExecution: &drillExecutionSummary{
			Adapter: "local", Assertions: 1, Passed: 1,
			Results: []harness.AssertionResult{{ID: "D-01.headline", Status: harness.AssertionPassed}},
		},
	}
	if err := writeJUnitReportFromReport(path, report); err != nil {
		t.Fatalf("writeJUnitReportFromReport() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "<testsuites") {
		t.Fatalf("written xml missing testsuites: %s", data)
	}
}

func TestDeterministicCaseTimeIsStable(t *testing.T) {
	if deterministicCaseTime("D-01.foo") != deterministicCaseTime("D-01.foo") {
		t.Fatalf("deterministicCaseTime() not deterministic")
	}
	if deterministicCaseTime("D-01.foo") == deterministicCaseTime("D-02.foo") {
		// Sanity: hashes should differ for different inputs in most cases.
		t.Logf("deterministicCaseTime collisions are allowed but unexpected here")
	}
}

func TestValidateJUnitXMLRoundTripsEmittedReport(t *testing.T) {
	report := gateReport{
		DrillExecution: &drillExecutionSummary{
			Adapter: "local", Assertions: 3, Passed: 1, Failed: 1, Skipped: 1,
			Results: []harness.AssertionResult{
				{ID: "D-01.headline", Status: harness.AssertionPassed},
				{ID: "D-02.headline", Status: harness.AssertionFailed, ReasonCode: "phase0.alert.disk-pressure", Diagnostic: "expected fail"},
				{ID: "D-03.headline", Status: harness.AssertionSkipped, ReasonCode: "phase0.skipped"},
			},
		},
	}
	doc := buildJUnitDocument(report)
	xmlBytes, err := renderJUnitXML(doc)
	if err != nil {
		t.Fatalf("renderJUnitXML() error = %v", err)
	}
	if err := ValidateJUnitXML(xmlBytes); err != nil {
		t.Fatalf("ValidateJUnitXML() error = %v, want round-trip success\n%s", err, xmlBytes)
	}
}

func TestValidateJUnitXMLRejectsEmptyPayload(t *testing.T) {
	if err := ValidateJUnitXML(nil); err == nil {
		t.Fatal("ValidateJUnitXML(nil) error = nil, want empty payload error")
	}
}

func TestValidateJUnitXMLRejectsMalformedXML(t *testing.T) {
	if err := ValidateJUnitXML([]byte("<not-xml")); err == nil {
		t.Fatal("ValidateJUnitXML() error = nil, want well-formed error")
	}
}

func TestValidateJUnitXMLRejectsWrongRoot(t *testing.T) {
	payload := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<wrong tests="0" failures="0" skipped="0"></wrong>`)
	err := ValidateJUnitXML(payload)
	if err == nil || !strings.Contains(err.Error(), "testsuites") {
		t.Fatalf("ValidateJUnitXML() error = %v, want root-element error", err)
	}
}

func TestValidateJUnitXMLRejectsMissingClassName(t *testing.T) {
	payload := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="phase0-gate" tests="1" failures="0" skipped="0">
  <testsuite name="D-01" tests="1" failures="0" skipped="0" time="0.001">
    <testcase name="case-1" time="0.001"></testcase>
  </testsuite>
</testsuites>`)
	err := ValidateJUnitXML(payload)
	if err == nil || !strings.Contains(err.Error(), "classname") {
		t.Fatalf("ValidateJUnitXML() error = %v, want classname error", err)
	}
}

func TestValidateJUnitXMLRejectsCountMismatch(t *testing.T) {
	payload := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="phase0-gate" tests="1" failures="1" skipped="0">
  <testsuite name="D-01" tests="1" failures="0" skipped="0" time="0.001">
    <testcase name="case-1" classname="D-01" time="0.001"></testcase>
  </testsuite>
</testsuites>`)
	err := ValidateJUnitXML(payload)
	if err == nil || !strings.Contains(err.Error(), "top-level") {
		t.Fatalf("ValidateJUnitXML() error = %v, want count mismatch error", err)
	}
}

func TestValidateJUnitXMLRejectsFailureMissingType(t *testing.T) {
	payload := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="phase0-gate" tests="1" failures="1" skipped="0">
  <testsuite name="D-01" tests="1" failures="1" skipped="0" time="0.001">
    <testcase name="case-1" classname="D-01" time="0.001">
      <failure message="m"></failure>
    </testcase>
  </testsuite>
</testsuites>`)
	err := ValidateJUnitXML(payload)
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("ValidateJUnitXML() error = %v, want failure type error", err)
	}
}
