package acme

import (
	"errors"
	"strings"
	"time"
)

type OrderStatus string

const (
	OrderStatusPending    OrderStatus = "pending"
	OrderStatusReady      OrderStatus = "ready"
	OrderStatusProcessing OrderStatus = "processing"
	OrderStatusValid      OrderStatus = "valid"
	OrderStatusInvalid    OrderStatus = "invalid"
)

type OrderState struct {
	ID                string      `json:"id,omitempty"`
	Domain            string      `json:"domain,omitempty"`
	Status            OrderStatus `json:"status"`
	AuthorizationURLs []string    `json:"authorizationUrls,omitempty"`
	CertificateURL    string      `json:"certificateUrl,omitempty"`
	ExpiresAt         time.Time   `json:"expiresAt,omitempty"`
	LastError         string      `json:"lastError,omitempty"`
}

func (state OrderState) Normalized() OrderState {
	state.ID = strings.TrimSpace(state.ID)
	state.Domain = normalizeDomain(state.Domain)
	state.CertificateURL = strings.TrimSpace(state.CertificateURL)
	state.LastError = strings.TrimSpace(state.LastError)

	urls := make([]string, 0, len(state.AuthorizationURLs))
	for _, url := range state.AuthorizationURLs {
		url = strings.TrimSpace(url)
		if url != "" {
			urls = append(urls, url)
		}
	}
	state.AuthorizationURLs = urls
	return state
}

func (state OrderState) Validate() error {
	state = state.Normalized()
	if state.Domain == "" {
		return errors.New("ACME order domain is required")
	}
	if !validOrderStatus(state.Status) {
		return errors.New("ACME order status is invalid")
	}
	if state.Status == OrderStatusValid && state.CertificateURL == "" {
		return errors.New("valid ACME order requires certificate URL")
	}
	if state.Status == OrderStatusInvalid && state.LastError == "" {
		return errors.New("invalid ACME order requires last error")
	}
	return nil
}

func validOrderStatus(status OrderStatus) bool {
	switch status {
	case OrderStatusPending, OrderStatusReady, OrderStatusProcessing, OrderStatusValid, OrderStatusInvalid:
		return true
	default:
		return false
	}
}
