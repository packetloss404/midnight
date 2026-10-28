// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const invitationOnboardingPath = v1alphaPrefix + "/invitations/onboard"

type InvitationOnboardingRequest struct {
	InvitationToken string `json:"invitation_token"`
	DisplayName     string `json:"display_name"`
	Password        string `json:"password"`
	RemoteAddr      string `json:"-"`
	RequestID       string `json:"-"`
}

func (InvitationOnboardingRequest) String() string {
	return "invitation onboarding request (credentials redacted)"
}
func (InvitationOnboardingRequest) GoString() string {
	return "invitation onboarding request (credentials redacted)"
}

// Onboarding never issues a session. Normal login retains the existing password,
// MFA, session-generation, and login-rate-limit policy.
type InvitationOnboardingResult struct {
	UserID     string              `json:"user_id"`
	Email      string              `json:"email"`
	Invitation identity.Invitation `json:"invitation"`
}

type InvitationOnboardingCoordinator interface {
	OnboardInvitation(context.Context, InvitationOnboardingRequest) (InvitationOnboardingResult, error)
}

func registerInvitationOnboardingRoutes(mux handlerRegistrar, coordinator InvitationOnboardingCoordinator) {
	mux.Handle(invitationOnboardingPath, invitationOnboardingHandler(coordinator))
}

func invitationOnboardingHandler(coordinator InvitationOnboardingCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if !identityInvitationHasNoQuery(r) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation onboarding does not accept query parameters", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable invitation onboarding is unavailable", nil)
			return
		}
		var input InvitationOnboardingRequest
		body, ok := readBoundedJSONRequestBodyWithLimit(w, r, "invalid invitation onboarding request", 4096)
		if !ok {
			return
		}
		defer clear(body)
		// Parser diagnostics may include attacker-controlled field names. Do
		// not echo any part of this credential-bearing document in an error.
		if strictJSONDecode(body, &input) != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid invitation onboarding request", nil)
			return
		}
		input.InvitationToken = strings.TrimSpace(input.InvitationToken)
		input.DisplayName = strings.TrimSpace(input.DisplayName)
		if input.DisplayName == "" || len(input.DisplayName) > 160 || len(input.Password) < 12 || len(input.Password) > 1024 || len(input.InvitationToken) > 128 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "use a name of at most 160 bytes and a password of 12–1024 bytes", nil)
			return
		}
		input.RemoteAddr = r.RemoteAddr
		input.RequestID = middleware.FromContext(r.Context())
		result, err := coordinator.OnboardInvitation(r.Context(), input)
		input.Password = ""
		input.InvitationToken = ""
		switch {
		case errors.Is(err, identity.ErrInvitationAccountExists):
			writeRequestError(w, r, http.StatusConflict, failurecodes.InvitationAccountExists, "This invited account already exists. Sign in and accept the invitation; its password was not changed.", nil)
		case errors.Is(err, identity.ErrLoginRateLimited):
			seconds := 900
			var limited *identity.LoginRateLimitError
			if errors.As(err, &limited) && limited.RetryAfter.Seconds() > 0 {
				seconds = int(limited.RetryAfter.Seconds()) + 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.AuthRateLimited, "invitation onboarding is temporarily rate limited", nil)
		case errors.Is(err, identity.ErrInvalidInvitation), errors.Is(err, identity.ErrInvitationNotFound), errors.Is(err, identity.ErrInvitationConsumed), errors.Is(err, identity.ErrInvitationRevoked), errors.Is(err, identity.ErrInvitationExpired), errors.Is(err, identity.ErrInvitationScopeNotActive):
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "This invitation is unavailable. Ask its sender for a current invitation.", nil)
		case err != nil:
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "invitation onboarding is unavailable", nil)
		default:
			WriteJSON(w, http.StatusCreated, result)
		}
	}
}
