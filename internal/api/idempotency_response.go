// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
)

// idempotencyResponseCapture delays a legacy mutation response until its exact
// terminal representation has been committed to the idempotency store. This
// prevents a successful mutation from reaching the client with an incomplete
// replay record, and ensures post-reservation validation/store failures become
// terminal replayable failures instead of leaving a RESERVED row behind.
type idempotencyResponseCapture struct {
	target      http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
}

func newIdempotencyResponseCapture(target http.ResponseWriter) *idempotencyResponseCapture {
	return &idempotencyResponseCapture{
		target: target,
		header: cloneHTTPHeader(target.Header()),
	}
}

func (c *idempotencyResponseCapture) Header() http.Header {
	return c.header
}

func (c *idempotencyResponseCapture) WriteHeader(statusCode int) {
	if c.wroteHeader {
		return
	}
	c.statusCode = statusCode
	c.wroteHeader = true
}

func (c *idempotencyResponseCapture) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.body.Write(body)
}

// Complete commits the buffered response and only then releases it to the
// underlying writer. All ordinary 2xx responses are successes; 4xx/5xx
// responses are stored failures and replay with the same body and stable
// transport headers.
func (c *idempotencyResponseCapture) Complete(
	r *http.Request,
	store idempotency.Store,
	ref idempotency.Reference,
) {
	if !c.wroteHeader {
		writeRequestError(c, r, http.StatusInternalServerError, failurecodes.InternalError, "mutation completed without a response", nil)
	}
	stored := idempotency.StoredResponse{
		StatusCode: c.statusCode,
		Header:     replayableResponseHeaders(c.header),
		Body:       append([]byte(nil), c.body.Bytes()...),
	}
	var err error
	if c.statusCode >= http.StatusOK && c.statusCode < http.StatusMultipleChoices {
		_, err = store.CompleteSuccess(r.Context(), ref, stored)
	} else {
		_, err = store.CompleteFailure(r.Context(), ref, stored)
	}
	if err != nil {
		// The response was never released, so fail honestly instead of telling
		// the client a result that cannot be replayed. The reservation remains
		// generation-fenced for operator repair if persistence itself is down.
		writeRequestError(c.target, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency response completion failed", nil)
		return
	}
	c.flush()
}

func (c *idempotencyResponseCapture) flush() {
	targetHeader := c.target.Header()
	for key, values := range c.header {
		targetHeader.Del(key)
		for _, value := range values {
			targetHeader.Add(key, value)
		}
	}
	c.target.WriteHeader(c.statusCode)
	_, _ = c.target.Write(c.body.Bytes())
}

func cloneHTTPHeader(header http.Header) http.Header {
	clone := make(http.Header, len(header))
	for key, values := range header {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

// Only deterministic representation/resource metadata belongs in a replay.
// Request-scoped middleware headers (request IDs, rate-limit counters, dates,
// cookies) must be regenerated for the retry rather than frozen for 24 hours.
func replayableResponseHeaders(header http.Header) http.Header {
	stored := make(http.Header)
	for _, key := range []string{
		"Cache-Control",
		"Content-Disposition",
		"Content-Language",
		"Content-Type",
		"ETag",
		"Last-Modified",
		"Link",
		"Location",
		"Retry-After",
	} {
		if values := header.Values(key); len(values) > 0 {
			stored[key] = append([]string(nil), values...)
		}
	}
	return stored
}

// reservationHasExactStoredResponse distinguishes exact terminal
// representations from historical status-only accepted rows. Several legacy
// long-running mutations attached an operation and completed idempotency with
// only a 202 status; those rows must retain their operation reconstruction
// fallback. New terminal failures and corrected successes carry a body and/or
// representation headers and must win over OperationID so replay cannot turn a
// stored RFC 9457 failure into a synthetic 202 operation response.
func reservationHasExactStoredResponse(reservation idempotency.Reservation) bool {
	if !reservation.IsTerminal() || reservation.Response == nil {
		return false
	}
	response := reservation.Response
	return len(response.Body) > 0 || len(response.Header) > 0 || response.StatusCode == http.StatusNoContent
}
