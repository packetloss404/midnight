package harness

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// AssertionStatus is the normalized outcome for a drill assertion.
type AssertionStatus string

const (
	AssertionPassed  AssertionStatus = "passed"
	AssertionFailed  AssertionStatus = "failed"
	AssertionSkipped AssertionStatus = "skipped"
)

// AssertionResult records the result of one drill check.
type AssertionResult struct {
	ID         string          `json:"id"`
	Status     AssertionStatus `json:"status"`
	ReasonCode string          `json:"reasonCode,omitempty"`
	Diagnostic string          `json:"diagnostic,omitempty"`
}

// DrillStatus is the normalized release-gate outcome for one drill.
type DrillStatus string

const (
	DrillPassed      DrillStatus = "passed"
	DrillFailed      DrillStatus = "failed"
	DrillBlocked     DrillStatus = "blocked"
	DrillPlaceholder DrillStatus = "placeholder"
)

// DrillAssertionResults groups assertion results for one drill.
type DrillAssertionResults struct {
	DrillID    string
	Assertions []AssertionResult
}

// DrillEvaluation records the computed release-gate status for one drill.
type DrillEvaluation struct {
	DrillID      string      `json:"drillId"`
	Status       DrillStatus `json:"status"`
	Passed       int         `json:"passed"`
	Failed       int         `json:"failed"`
	Blocked      int         `json:"blocked"`
	Placeholders int         `json:"placeholders"`
}

// Passed reports whether the assertion completed successfully.
func (r AssertionResult) Passed() bool {
	return r.Status == AssertionPassed
}

// EvaluateDrills computes drill-level statuses from grouped assertion results.
// No assertions means the drill remains a placeholder; skipped assertions block
// the drill unless a failure is also present.
func EvaluateDrills(drills []DrillAssertionResults) ([]DrillEvaluation, error) {
	evaluations := make([]DrillEvaluation, 0, len(drills))
	for i, drill := range drills {
		drillID := drill.DrillID
		if drillID == "" {
			drillID = fmt.Sprintf("drill-%d", i+1)
		}
		evaluation := DrillEvaluation{DrillID: drillID}
		if len(drill.Assertions) == 0 {
			evaluation.Status = DrillPlaceholder
			evaluation.Placeholders = 1
			evaluations = append(evaluations, evaluation)
			continue
		}

		for j, assertion := range drill.Assertions {
			switch assertion.Status {
			case AssertionPassed:
				evaluation.Passed++
			case AssertionFailed:
				evaluation.Failed++
			case AssertionSkipped:
				evaluation.Blocked++
			default:
				name := assertion.ID
				if name == "" {
					name = fmt.Sprintf("assertion-%d", j+1)
				}
				return nil, fmt.Errorf("drill %s assertion %s has unknown status %q", drillID, name, assertion.Status)
			}
		}
		switch {
		case evaluation.Failed > 0:
			evaluation.Status = DrillFailed
		case evaluation.Blocked > 0:
			evaluation.Status = DrillBlocked
		default:
			evaluation.Status = DrillPassed
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, nil
}

type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Skipped   int             `xml:"skipped,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name    string        `xml:"name,attr"`
	Failure *junitFailure `xml:"failure,omitempty"`
	Skipped *junitSkipped `xml:"skipped,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

// EmitJUnitXML renders drill assertion results as a compact JUnit XML document.
func EmitJUnitXML(results []AssertionResult) ([]byte, error) {
	suite := junitTestSuite{Name: "phase0-drills", Tests: len(results), TestCases: make([]junitTestCase, 0, len(results))}
	for i, result := range results {
		name := result.ID
		if name == "" {
			name = fmt.Sprintf("assertion-%d", i+1)
		}
		testCase := junitTestCase{Name: name}
		switch result.Status {
		case AssertionPassed:
		case AssertionFailed:
			suite.Failures++
			testCase.Failure = &junitFailure{Message: result.ReasonCode, Text: result.Diagnostic}
		case AssertionSkipped:
			suite.Skipped++
			testCase.Skipped = &junitSkipped{Message: result.ReasonCode}
		default:
			return nil, fmt.Errorf("assertion %s has unknown status %q", name, result.Status)
		}
		suite.TestCases = append(suite.TestCases, testCase)
	}

	var out bytes.Buffer
	out.WriteString(xml.Header)
	encoder := xml.NewEncoder(&out)
	encoder.Indent("", "  ")
	if err := encoder.Encode(junitTestSuites{Suites: []junitTestSuite{suite}}); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
