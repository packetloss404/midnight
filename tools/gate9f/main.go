// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Command gate9f scores an independent Midnight usability study against the
// fixed Gate 9F acceptance thresholds. It deliberately accepts pseudonymous
// observations only; names, email addresses, recordings, and consent records
// belong in the study operator's access-controlled research system.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	minimumParticipants = 12
	minimumMeanRating   = 8.8
	minimumMedianRating = 9.0
	minimumSegmentMean  = 8.5
	minimumCompletion   = 0.90
	minimumServing      = 1.00
	minimumPrediction   = 0.90
	minimumConfidence   = 6.0
	minimumSUS          = 85.0
)

var requiredSegments = map[string]int{
	"railway-user":  4,
	"new-user":      4,
	"operator":      2,
	"accessibility": 2,
}

type study struct {
	StudyID       string        `json:"study_id"`
	ProductCommit string        `json:"product_commit"`
	Environment   string        `json:"environment"`
	Safety        safetyChecks  `json:"safety"`
	Participants  []participant `json:"participants"`
}

type safetyChecks struct {
	NoSecretExposure        bool `json:"no_secret_exposure"`
	NoCrossTenantAccess     bool `json:"no_cross_tenant_access"`
	NoDesiredStateAsRuntime bool `json:"no_desired_state_as_observed_runtime"`
}

type participant struct {
	ID                    string                 `json:"id"`
	Segment               string                 `json:"segment"`
	OverallRating         float64                `json:"overall_rating"`
	RailwayFamiliarity    float64                `json:"railway_familiarity"`
	NewUserEase           float64                `json:"new_user_ease"`
	UXCoherence           float64                `json:"ux_coherence"`
	SUSAnswers            []int                  `json:"sus_answers"`
	Tasks                 []taskResult           `json:"tasks"`
	AccessibilityFindings []accessibilityFinding `json:"accessibility_findings"`
}

type taskResult struct {
	TaskID                 int   `json:"task_id"`
	CompletedWithoutHelp   bool  `json:"completed_without_help"`
	DurationSeconds        int   `json:"duration_seconds"`
	WrongTurns             int   `json:"wrong_turns"`
	Confidence             int   `json:"confidence"`
	ServingReleaseCorrect  *bool `json:"serving_release_correct,omitempty"`
	ApplyPredictionCorrect *bool `json:"apply_prediction_correct,omitempty"`
}

type accessibilityFinding struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

type score struct {
	ParticipantCount int
	MeanRating       float64
	MedianRating     float64
	SegmentMeans     map[string]float64
	SegmentCounts    map[string]int
	CompletionRate   float64
	ServingRate      float64
	PredictionRate   float64
	MedianConfidence float64
	MeanSUS          float64
	BlockingFindings int
	Passes           map[string]bool
}

func main() {
	input := flag.String("input", "", "path to the pseudonymous Gate 9F results JSON")
	flag.Parse()
	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/gate9f -input PATH")
		os.Exit(2)
	}

	file, err := os.Open(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate9f: open input: %v\n", err)
		os.Exit(2)
	}
	defer file.Close()

	var value study
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		fmt.Fprintf(os.Stderr, "gate9f: decode input: %v\n", err)
		os.Exit(2)
	}
	result, err := scoreStudy(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate9f: invalid study: %v\n", err)
		os.Exit(2)
	}
	writeReport(os.Stdout, value, result)
	for _, pass := range result.Passes {
		if !pass {
			os.Exit(1)
		}
	}
}

func scoreStudy(value study) (score, error) {
	if strings.TrimSpace(value.StudyID) == "" || strings.TrimSpace(value.ProductCommit) == "" || strings.TrimSpace(value.Environment) == "" {
		return score{}, errors.New("study_id, product_commit, and environment are required")
	}
	if len(value.Participants) == 0 {
		return score{}, errors.New("participants are required")
	}

	result := score{
		ParticipantCount: len(value.Participants),
		SegmentMeans:     map[string]float64{},
		SegmentCounts:    map[string]int{},
		Passes:           map[string]bool{},
	}
	var ratings, confidences, susScores []float64
	var coreCompleted, coreTotal, servingCorrect, servingTotal, predictionCorrect, predictionTotal int
	seen := map[string]struct{}{}

	for _, person := range value.Participants {
		if strings.TrimSpace(person.ID) == "" {
			return score{}, errors.New("every participant requires a pseudonymous id")
		}
		if _, ok := seen[person.ID]; ok {
			return score{}, fmt.Errorf("duplicate participant id %q", person.ID)
		}
		seen[person.ID] = struct{}{}
		if _, ok := requiredSegments[person.Segment]; !ok {
			return score{}, fmt.Errorf("participant %q has unknown segment %q", person.ID, person.Segment)
		}
		if !within(person.OverallRating, 1, 10) || !within(person.RailwayFamiliarity, 1, 10) || !within(person.NewUserEase, 1, 10) || !within(person.UXCoherence, 1, 10) {
			return score{}, fmt.Errorf("participant %q ratings must be between 1 and 10", person.ID)
		}
		if len(person.Tasks) != 10 {
			return score{}, fmt.Errorf("participant %q must have exactly 10 task observations", person.ID)
		}
		result.SegmentCounts[person.Segment]++
		result.SegmentMeans[person.Segment] += person.OverallRating
		ratings = append(ratings, person.OverallRating)

		tasks := map[int]taskResult{}
		for _, task := range person.Tasks {
			if task.TaskID < 1 || task.TaskID > 10 {
				return score{}, fmt.Errorf("participant %q has invalid task_id %d", person.ID, task.TaskID)
			}
			if _, ok := tasks[task.TaskID]; ok {
				return score{}, fmt.Errorf("participant %q repeats task_id %d", person.ID, task.TaskID)
			}
			if task.DurationSeconds < 0 || task.WrongTurns < 0 || task.Confidence < 1 || task.Confidence > 7 {
				return score{}, fmt.Errorf("participant %q task %d has invalid measurements", person.ID, task.TaskID)
			}
			tasks[task.TaskID] = task
			confidences = append(confidences, float64(task.Confidence))
			if task.TaskID <= 9 {
				coreTotal++
				if task.CompletedWithoutHelp {
					coreCompleted++
				}
			}
		}
		if tasks[2].ServingReleaseCorrect == nil {
			return score{}, fmt.Errorf("participant %q task 2 requires serving_release_correct", person.ID)
		}
		servingTotal++
		if *tasks[2].ServingReleaseCorrect {
			servingCorrect++
		}
		for _, taskID := range []int{5, 6} {
			if tasks[taskID].ApplyPredictionCorrect == nil {
				return score{}, fmt.Errorf("participant %q task %d requires apply_prediction_correct", person.ID, taskID)
			}
			predictionTotal++
			if *tasks[taskID].ApplyPredictionCorrect {
				predictionCorrect++
			}
		}

		sus, err := susScore(person.SUSAnswers)
		if err != nil {
			return score{}, fmt.Errorf("participant %q: %w", person.ID, err)
		}
		susScores = append(susScores, sus)
		for _, finding := range person.AccessibilityFindings {
			switch finding.Severity {
			case "none", "minor":
			case "serious", "critical", "blocking":
				result.BlockingFindings++
			default:
				return score{}, fmt.Errorf("participant %q has unknown accessibility severity %q", person.ID, finding.Severity)
			}
		}
	}

	for segment, total := range result.SegmentMeans {
		result.SegmentMeans[segment] = total / float64(result.SegmentCounts[segment])
	}
	result.MeanRating = mean(ratings)
	result.MedianRating = median(ratings)
	result.CompletionRate = ratio(coreCompleted, coreTotal)
	result.ServingRate = ratio(servingCorrect, servingTotal)
	result.PredictionRate = ratio(predictionCorrect, predictionTotal)
	result.MedianConfidence = median(confidences)
	result.MeanSUS = mean(susScores)

	segmentsPass := true
	for segment, minimum := range requiredSegments {
		if result.SegmentCounts[segment] < minimum || result.SegmentMeans[segment] < minimumSegmentMean {
			segmentsPass = false
		}
	}
	result.Passes = map[string]bool{
		"participants":       result.ParticipantCount >= minimumParticipants,
		"segment thresholds": segmentsPass,
		"mean rating":        result.MeanRating >= minimumMeanRating,
		"median rating":      result.MedianRating >= minimumMedianRating,
		"core completion":    result.CompletionRate >= minimumCompletion,
		"serving release":    result.ServingRate >= minimumServing,
		"apply prediction":   result.PredictionRate >= minimumPrediction,
		"confidence":         result.MedianConfidence >= minimumConfidence,
		"SUS":                result.MeanSUS >= minimumSUS,
		"accessibility":      result.BlockingFindings == 0,
		"secret boundary":    value.Safety.NoSecretExposure,
		"tenant boundary":    value.Safety.NoCrossTenantAccess,
		"state truthfulness": value.Safety.NoDesiredStateAsRuntime,
	}
	return result, nil
}

func susScore(answers []int) (float64, error) {
	if len(answers) != 10 {
		return 0, errors.New("sus_answers must contain exactly 10 answers")
	}
	total := 0
	for index, answer := range answers {
		if answer < 1 || answer > 5 {
			return 0, errors.New("SUS answers must be between 1 and 5")
		}
		if index%2 == 0 {
			total += answer - 1
		} else {
			total += 5 - answer
		}
	}
	return float64(total) * 2.5, nil
}

func writeReport(w io.Writer, value study, result score) {
	fmt.Fprintf(w, "# Gate 9F score: %s\n\n", value.StudyID)
	fmt.Fprintf(w, "- Product commit: `%s`\n- Environment: %s\n- Participants: %d\n\n", value.ProductCommit, value.Environment, result.ParticipantCount)
	fmt.Fprintln(w, "| Measure | Result | Threshold | Verdict |")
	fmt.Fprintln(w, "| --- | ---: | ---: | --- |")
	rows := []struct{ name, value, threshold, key string }{
		{"Mean overall rating", fmt.Sprintf("%.2f/10", result.MeanRating), ">= 8.8", "mean rating"},
		{"Median overall rating", fmt.Sprintf("%.2f/10", result.MedianRating), ">= 9.0", "median rating"},
		{"Core task completion", percent(result.CompletionRate), ">= 90%", "core completion"},
		{"Serving release identification", percent(result.ServingRate), "100%", "serving release"},
		{"Apply/deploy prediction", percent(result.PredictionRate), ">= 90%", "apply prediction"},
		{"Median confidence", fmt.Sprintf("%.2f/7", result.MedianConfidence), ">= 6/7", "confidence"},
		{"Mean SUS", fmt.Sprintf("%.2f", result.MeanSUS), ">= 85", "SUS"},
		{"Serious/critical/blocking accessibility findings", fmt.Sprintf("%d", result.BlockingFindings), "0", "accessibility"},
		{"No secret exposure observed", fmt.Sprintf("%t", value.Safety.NoSecretExposure), "true", "secret boundary"},
		{"No cross-tenant access observed", fmt.Sprintf("%t", value.Safety.NoCrossTenantAccess), "true", "tenant boundary"},
		{"Desired state never shown as observed runtime", fmt.Sprintf("%t", value.Safety.NoDesiredStateAsRuntime), "true", "state truthfulness"},
	}
	for _, row := range rows {
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", row.name, row.value, row.threshold, verdict(result.Passes[row.key]))
	}
	fmt.Fprint(w, "\n## Participant segments\n\n")
	fmt.Fprintln(w, "| Segment | Count | Mean rating | Required | Verdict |")
	fmt.Fprintln(w, "| --- | ---: | ---: | ---: | --- |")
	segments := make([]string, 0, len(requiredSegments))
	for segment := range requiredSegments {
		segments = append(segments, segment)
	}
	sort.Strings(segments)
	for _, segment := range segments {
		pass := result.SegmentCounts[segment] >= requiredSegments[segment] && result.SegmentMeans[segment] >= minimumSegmentMean
		fmt.Fprintf(w, "| %s | %d | %.2f/10 | %d participants; >= 8.5 | %s |\n", segment, result.SegmentCounts[segment], result.SegmentMeans[segment], requiredSegments[segment], verdict(pass))
	}
	overall := true
	for _, pass := range result.Passes {
		overall = overall && pass
	}
	fmt.Fprintf(w, "\n**Overall verdict: %s.**\n", verdict(overall))
}

func within(value, minimum, maximum float64) bool { return value >= minimum && value <= maximum }
func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
func mean(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}
func median(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}
func percent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
func verdict(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}
