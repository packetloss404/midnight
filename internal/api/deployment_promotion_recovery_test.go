// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/midnight/midnight/internal/db/pg"
)

func TestCommittedPromotionVerificationResult(t *testing.T) {
	indeterminate := fmt.Errorf(
		"%w: read-only commit acknowledgement lost",
		pg.ErrTxCommitIndeterminate,
	)
	definite := errors.New("read-only verification failed")
	for _, test := range []struct {
		name        string
		matched     bool
		err         error
		wantMatched bool
		wantErr     bool
	}{
		{name: "proved", matched: true, wantMatched: true},
		{name: "not proved", wantMatched: false},
		{
			name:        "proved before read-only acknowledgement loss",
			matched:     true,
			err:         indeterminate,
			wantMatched: true,
		},
		{
			name:    "ambiguous verification without proof",
			err:     indeterminate,
			wantErr: true,
		},
		{
			name:    "definite verification failure",
			matched: true,
			err:     definite,
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			matched, err := committedPromotionVerificationResult(
				test.matched,
				test.err,
			)
			if matched != test.wantMatched ||
				(err != nil) != test.wantErr {
				t.Fatalf(
					"result matched/error=%v/%v, want %v/error=%v",
					matched,
					err,
					test.wantMatched,
					test.wantErr,
				)
			}
		})
	}
}
