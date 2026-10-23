package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midnight/midnight/test/drills/harness"
)

// junitDocument is a minimal JUnit XML document that captures per-drill
// timings (deterministic placeholder), per-failure messages, and a top-level
// tests/failures/skipped summary. The schema is intentionally a small subset
// that can be parsed by every common JUnit consumer.
type junitDocument struct {
	XMLName  xml.Name        `xml:"testsuites"`
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Skipped  int             `xml:"skipped,attr"`
	Suites   []junitSuiteDoc `xml:"testsuite"`
}

type junitSuiteDoc struct {
	Name     string         `xml:"name,attr"`
	Tests    int            `xml:"tests,attr"`
	Failures int            `xml:"failures,attr"`
	Skipped  int            `xml:"skipped,attr"`
	Time     string         `xml:"time,attr"`
	Cases    []junitCaseDoc `xml:"testcase"`
}

type junitCaseDoc struct {
	Name      string           `xml:"name,attr"`
	ClassName string           `xml:"classname,attr"`
	Time      string           `xml:"time,attr"`
	Failure   *junitFailureDoc `xml:"failure,omitempty"`
	Skipped   *junitSkippedDoc `xml:"skipped,omitempty"`
}

type junitFailureDoc struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitSkippedDoc struct {
	Message string `xml:"message,attr,omitempty"`
}

// deterministicCaseTime returns a stable per-case timing value used as a
// placeholder until live drill timings exist. The value is based on the
// assertion ID so two identical reports produce byte-identical XML.
func deterministicCaseTime(id string) string {
	hash := 0
	for _, r := range id {
		hash = hash*31 + int(r)
	}
	if hash < 0 {
		hash = -hash
	}
	// Bound the deterministic placeholder timing to [0.001, 0.500] seconds.
	ms := (hash % 500) + 1
	return fmt.Sprintf("%.3f", float64(ms)/1000.0)
}

// buildJUnitDocument renders a gateReport as a junitDocument suitable for
// downstream test reporters. It groups assertions by drill ID and emits
// per-assertion testcases with deterministic placeholder times.
func buildJUnitDocument(report gateReport) junitDocument {
	doc := junitDocument{Name: "phase0-gate"}
	if report.DrillExecution == nil {
		return doc
	}
	byDrill := make(map[string][]harness.AssertionResult)
	order := []string{}
	for _, result := range report.DrillExecution.Results {
		drillID := result.ID
		if idx := strings.IndexByte(drillID, '.'); idx > 0 {
			drillID = drillID[:idx]
		}
		if _, ok := byDrill[drillID]; !ok {
			order = append(order, drillID)
		}
		byDrill[drillID] = append(byDrill[drillID], result)
	}
	sort.Strings(order)
	for _, drillID := range order {
		assertions := byDrill[drillID]
		suite := junitSuiteDoc{Name: drillID, Tests: len(assertions)}
		var suiteSeconds float64
		for _, a := range assertions {
			caseDoc := junitCaseDoc{
				Name:      a.ID,
				ClassName: drillID,
				Time:      deterministicCaseTime(a.ID),
			}
			switch a.Status {
			case harness.AssertionPassed:
			case harness.AssertionFailed:
				suite.Failures++
				doc.Failures++
				caseDoc.Failure = &junitFailureDoc{
					Message: a.ReasonCode,
					Type:    "DrillAssertionFailed",
					Body:    a.Diagnostic,
				}
			case harness.AssertionSkipped:
				suite.Skipped++
				doc.Skipped++
				caseDoc.Skipped = &junitSkippedDoc{Message: a.ReasonCode}
			}
			var t float64
			fmt.Sscanf(caseDoc.Time, "%f", &t)
			suiteSeconds += t
			suite.Cases = append(suite.Cases, caseDoc)
		}
		suite.Time = fmt.Sprintf("%.3f", suiteSeconds)
		doc.Tests += suite.Tests
		doc.Suites = append(doc.Suites, suite)
	}
	return doc
}

// renderJUnitXML serializes a junitDocument as an XML byte payload.
func renderJUnitXML(doc junitDocument) ([]byte, error) {
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// validateJUnitDocument runs an offline schema-shape check against the
// rendered JUnit XML: the root must be testsuites, the tests/failures/skipped
// counts must be consistent, and every testcase must have a name and a
// non-empty time attribute. This is a minimal offline replacement for the
// JUnit XSD.
func validateJUnitDocument(doc junitDocument) error {
	if doc.Name == "" {
		return errors.New("junit document name is required")
	}
	if doc.Tests < 0 || doc.Failures < 0 || doc.Skipped < 0 {
		return errors.New("junit counts must be non-negative")
	}
	if doc.Failures > doc.Tests || doc.Skipped > doc.Tests {
		return errors.New("junit failures/skipped cannot exceed tests")
	}
	totalTests, totalFailures, totalSkipped := 0, 0, 0
	for _, suite := range doc.Suites {
		if strings.TrimSpace(suite.Name) == "" {
			return errors.New("junit testsuite name is required")
		}
		if len(suite.Cases) != suite.Tests {
			return fmt.Errorf("junit testsuite %s case count %d != tests %d", suite.Name, len(suite.Cases), suite.Tests)
		}
		caseFailures, caseSkipped := 0, 0
		for _, c := range suite.Cases {
			if strings.TrimSpace(c.Name) == "" {
				return fmt.Errorf("junit testsuite %s case name is required", suite.Name)
			}
			if strings.TrimSpace(c.Time) == "" {
				return fmt.Errorf("junit testsuite %s case %s time is required", suite.Name, c.Name)
			}
			if c.Failure != nil {
				caseFailures++
			}
			if c.Skipped != nil {
				caseSkipped++
			}
		}
		if caseFailures != suite.Failures {
			return fmt.Errorf("junit testsuite %s failure count %d != case failures %d", suite.Name, suite.Failures, caseFailures)
		}
		if caseSkipped != suite.Skipped {
			return fmt.Errorf("junit testsuite %s skipped count %d != case skipped %d", suite.Name, suite.Skipped, caseSkipped)
		}
		totalTests += suite.Tests
		totalFailures += suite.Failures
		totalSkipped += suite.Skipped
	}
	if totalTests != doc.Tests || totalFailures != doc.Failures || totalSkipped != doc.Skipped {
		return errors.New("junit top-level counts disagree with sum of suites")
	}
	return nil
}

// ValidateJUnitXML parses an emitted JUnit XML payload and verifies that it
// matches the minimal JUnit shape that downstream consumers (GitHub Actions,
// CI dashboards, the phase0 release-evidence index) require: the root element
// MUST be <testsuites> with non-negative tests/failures/skipped counts; every
// <testsuite> child MUST carry a non-empty name; every <testcase> child MUST
// carry both a name and a classname attribute and a non-empty time attribute;
// any <failure> child carries a non-empty type attribute; <skipped> children
// are well-formed empty elements. Per-suite and top-level counts MUST agree
// with the actual case content. Returns a deterministic error describing the
// first violation found, or nil when the document round-trips cleanly.
//
// ValidateJUnitXML is the public counterpart of validateJUnitDocument: where
// validateJUnitDocument validates a typed in-memory document, ValidateJUnitXML
// works directly on the wire bytes. The two checks are deliberately equivalent
// so that emit-then-validate round-trips never disagree.
func ValidateJUnitXML(data []byte) error {
	if len(data) == 0 {
		return errors.New("junit xml payload is empty")
	}
	var doc junitDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("junit xml is not well-formed: %w", err)
	}
	if doc.XMLName.Local != "testsuites" {
		return fmt.Errorf("junit xml root must be <testsuites>, got <%s>", doc.XMLName.Local)
	}
	if doc.Tests < 0 || doc.Failures < 0 || doc.Skipped < 0 {
		return errors.New("junit xml counts must be non-negative")
	}
	if doc.Failures > doc.Tests || doc.Skipped > doc.Tests {
		return errors.New("junit xml failures/skipped cannot exceed tests")
	}
	totalTests, totalFailures, totalSkipped := 0, 0, 0
	for _, suite := range doc.Suites {
		if strings.TrimSpace(suite.Name) == "" {
			return errors.New("junit xml <testsuite> name attribute is required")
		}
		if suite.Tests < 0 || suite.Failures < 0 || suite.Skipped < 0 {
			return fmt.Errorf("junit xml <testsuite> %s counts must be non-negative", suite.Name)
		}
		if len(suite.Cases) != suite.Tests {
			return fmt.Errorf("junit xml <testsuite> %s case count %d != tests %d", suite.Name, len(suite.Cases), suite.Tests)
		}
		caseFailures, caseSkipped := 0, 0
		for _, c := range suite.Cases {
			if strings.TrimSpace(c.Name) == "" {
				return fmt.Errorf("junit xml <testcase> in suite %s missing required name attribute", suite.Name)
			}
			if strings.TrimSpace(c.ClassName) == "" {
				return fmt.Errorf("junit xml <testcase> %s in suite %s missing required classname attribute", c.Name, suite.Name)
			}
			if strings.TrimSpace(c.Time) == "" {
				return fmt.Errorf("junit xml <testcase> %s in suite %s missing required time attribute", c.Name, suite.Name)
			}
			if c.Failure != nil {
				caseFailures++
				if strings.TrimSpace(c.Failure.Type) == "" {
					return fmt.Errorf("junit xml <failure> in case %s missing required type attribute", c.Name)
				}
			}
			if c.Skipped != nil {
				caseSkipped++
			}
		}
		if caseFailures != suite.Failures {
			return fmt.Errorf("junit xml <testsuite> %s failure count %d != case failures %d", suite.Name, suite.Failures, caseFailures)
		}
		if caseSkipped != suite.Skipped {
			return fmt.Errorf("junit xml <testsuite> %s skipped count %d != case skipped %d", suite.Name, suite.Skipped, caseSkipped)
		}
		totalTests += suite.Tests
		totalFailures += suite.Failures
		totalSkipped += suite.Skipped
	}
	if totalTests != doc.Tests || totalFailures != doc.Failures || totalSkipped != doc.Skipped {
		return errors.New("junit xml top-level counts disagree with sum of suites")
	}
	return nil
}

// writeJUnitReportFromReport writes the JUnit XML payload built from the
// gate report to the supplied path. The path must end in .xml and its parent
// directory must already exist; this matches the behavior of the
// release-evidence path validator.
func writeJUnitReportFromReport(path string, report gateReport) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("junit path is required")
	}
	if strings.ToLower(filepath.Ext(path)) != ".xml" {
		return fmt.Errorf("junit path must end in .xml: %s", path)
	}
	parent := filepath.Dir(path)
	if parent != "." && parent != "" {
		info, err := os.Stat(parent)
		if err != nil {
			return fmt.Errorf("junit parent does not exist: %s", parent)
		}
		if !info.IsDir() {
			return fmt.Errorf("junit parent is not a directory: %s", parent)
		}
	}
	doc := buildJUnitDocument(report)
	if err := validateJUnitDocument(doc); err != nil {
		return err
	}
	data, err := renderJUnitXML(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
