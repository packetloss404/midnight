// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import "testing"

func TestSUSScore(t *testing.T) {
	got, err := susScore([]int{5, 1, 5, 1, 5, 1, 5, 1, 5, 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Fatalf("score = %v, want 100", got)
	}
}

func TestScoreStudyEnforcesServingAndApplyPredictions(t *testing.T) {
	value := passingStudy()
	result, err := scoreStudy(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, pass := range result.Passes {
		if !pass {
			t.Fatalf("%s failed in passing fixture", name)
		}
	}

	wrong := false
	value.Participants[0].Tasks[1].ServingReleaseCorrect = &wrong
	result, err = scoreStudy(value)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passes["serving release"] {
		t.Fatal("serving-release threshold passed with one wrong answer")
	}
}

func TestScoreStudyEnforcesSafetyChecks(t *testing.T) {
	value := passingStudy()
	value.Safety.NoSecretExposure = false

	result, err := scoreStudy(value)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passes["secret boundary"] {
		t.Fatal("secret boundary passed when the study reported exposure")
	}
}

func passingStudy() study {
	segments := []string{
		"railway-user", "railway-user", "railway-user", "railway-user",
		"new-user", "new-user", "new-user", "new-user",
		"operator", "operator", "accessibility", "accessibility",
	}
	value := study{
		StudyID:       "test",
		ProductCommit: "0123456",
		Environment:   "test fixture",
		Safety: safetyChecks{
			NoSecretExposure:        true,
			NoCrossTenantAccess:     true,
			NoDesiredStateAsRuntime: true,
		},
	}
	for index, segment := range segments {
		person := participant{
			ID:                 segment + string(rune('a'+index)),
			Segment:            segment,
			OverallRating:      9,
			RailwayFamiliarity: 5,
			NewUserEase:        9,
			UXCoherence:        9,
			SUSAnswers:         []int{5, 1, 5, 1, 5, 1, 5, 1, 5, 1},
		}
		for taskID := 1; taskID <= 10; taskID++ {
			correct := true
			task := taskResult{TaskID: taskID, CompletedWithoutHelp: true, DurationSeconds: 30, Confidence: 7}
			if taskID == 2 {
				task.ServingReleaseCorrect = &correct
			}
			if taskID == 5 || taskID == 6 {
				task.ApplyPredictionCorrect = &correct
			}
			person.Tasks = append(person.Tasks, task)
		}
		value.Participants = append(value.Participants, person)
	}
	return value
}
