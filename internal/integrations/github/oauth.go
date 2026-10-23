// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ExchangeUserCode exchanges GitHub's short-lived callback code for a user
// access token. The token remains a redacting Secret and is used only for the
// immediate installation-ownership proof.
func (c *AppClient) ExchangeUserCode(ctx context.Context, code string) (Secret, error) {
	if err := c.cfg.ValidateSetup(); err != nil {
		return Secret{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 1024 {
		return Secret{}, fmt.Errorf("%w: OAuth code is invalid", ErrAppConfigInvalid)
	}
	form := url.Values{
		"client_id":     []string{c.cfg.ClientID},
		"client_secret": []string{c.cfg.ClientSecret},
		"code":          []string{code},
		"redirect_uri":  []string{c.cfg.CallbackURL},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.cfg.WebBase()+"/login/oauth/access_token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return Secret{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.doer.Do(req)
	if err != nil {
		return Secret{}, errors.New(newRedactor(code, c.cfg.ClientSecret).redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return Secret{}, err
	}
	redactor := newRedactor(code, c.cfg.ClientSecret)
	if resp.StatusCode/100 != 2 {
		return Secret{}, &AppHTTPError{StatusCode: resp.StatusCode, Body: redactor.redact(truncate(body, 256))}
	}
	var raw struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Secret{}, fmt.Errorf("%w: decode OAuth token: %v", ErrAppHTTP, err)
	}
	if strings.TrimSpace(raw.AccessToken) == "" {
		return Secret{}, fmt.Errorf("%w: OAuth exchange was rejected (%s)", ErrAppHTTP, redactor.redact(raw.Error))
	}
	return NewSecret(raw.AccessToken), nil
}

// UserCanAccessInstallation asks GitHub whether this App's user access token
// can access one exact installation. It deliberately ignores callback account
// and owner query values; GitHub is the authority for this association.
func (c *AppClient) UserCanAccessInstallation(
	ctx context.Context,
	token Secret,
	installationID int64,
) error {
	if installationID <= 0 || strings.TrimSpace(token.Value()) == "" {
		return fmt.Errorf("%w: user installation proof is invalid", ErrAppConfigInvalid)
	}
	endpoint := c.cfg.APIBase() + "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories?per_page=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return errors.New(scrubSecret(err.Error(), token))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &AppHTTPError{StatusCode: resp.StatusCode, Body: scrubSecret(truncate(body, 256), token)}
	}
	return nil
}
