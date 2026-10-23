// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type safeCommitError struct{}

func (safeCommitError) Error() string     { return "commit was not sent" }
func (safeCommitError) SafeToRetry() bool { return true }

func TestCommitOutcomeIndeterminateClassification(t *testing.T) {
	networkAckLost := errors.New("connection reset while reading commit acknowledgement")
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", want: false},
		{name: "explicit rollback", err: pgx.ErrTxCommitRollback, want: false},
		{name: "server rejection", err: &pgconn.PgError{Code: "40001", Message: "serialization failure"}, want: false},
		{name: "not sent", err: safeCommitError{}, want: false},
		{name: "acknowledgement lost", err: networkAckLost, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := commitOutcomeIndeterminate(test.err); got != test.want {
				t.Fatalf("commitOutcomeIndeterminate(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
	wrapped := &txCommitIndeterminateError{cause: networkAckLost}
	if !errors.Is(wrapped, ErrTxCommitIndeterminate) ||
		!errors.Is(wrapped, networkAckLost) {
		t.Fatal("indeterminate commit error did not preserve sentinel and cause")
	}
}
