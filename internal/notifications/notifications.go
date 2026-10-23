package notifications

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

type ChannelType string

const (
	ChannelSlackWebhook ChannelType = "slack_webhook"
	ChannelSMTP         ChannelType = "smtp"
)

type EventType string

const (
	EventDeploymentSucceeded EventType = "deployment.succeeded"
	EventDeploymentFailed    EventType = "deployment.failed"
	EventPreviewCreated      EventType = "preview.created"
	EventPreviewArchived     EventType = "preview.archived"
)

type ChannelConfig struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	Type        ChannelType  `json:"type"`
	DisplayName string       `json:"display_name"`
	Slack       *SlackConfig `json:"slack,omitempty"`
	SMTP        *SMTPConfig  `json:"smtp,omitempty"`
}

type SlackConfig struct {
	WebhookSecretRef string `json:"webhook_secret_ref"`
	DefaultChannel   string `json:"default_channel,omitempty"`
}

type SMTPConfig struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	TLS               bool   `json:"tls"`
	UsernameSecretRef string `json:"username_secret_ref,omitempty"`
	PasswordSecretRef string `json:"password_secret_ref,omitempty"`
	From              string `json:"from"`
}

type Subscription struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	ChannelID   string      `json:"channel_id"`
	EventTypes  []EventType `json:"event_types"`
	MutedUntil  time.Time   `json:"muted_until,omitempty"`
}

type DeliveryPreview struct {
	ChannelID     string            `json:"channel_id"`
	WorkspaceID   string            `json:"workspace_id"`
	Provider      ChannelType       `json:"provider"`
	EventType     EventType         `json:"event_type"`
	Suppressed    bool              `json:"suppressed"`
	FailureCode   failurecodes.Code `json:"failure_code"`
	AuditActions  []string          `json:"audit_actions"`
	PayloadFields []string          `json:"payload_fields"`
}

type CatalogEntry struct {
	EventType   EventType `json:"event_type"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	SampleTitle string    `json:"sample_title"`
}

type TestSendSample struct {
	ChannelID string            `json:"channel_id"`
	EventType EventType         `json:"event_type"`
	Subject   string            `json:"subject"`
	Text      string            `json:"text"`
	Fields    map[string]string `json:"fields"`
}

func SubscriptionCatalog() []CatalogEntry {
	return []CatalogEntry{
		{EventType: EventDeploymentSucceeded, DisplayName: "Deployment succeeded", Description: "A deployment finished successfully and passed readiness checks.", SampleTitle: "Deployment succeeded for checkout"},
		{EventType: EventDeploymentFailed, DisplayName: "Deployment failed", Description: "A deployment failed planning, rollout, or readiness validation.", SampleTitle: "Deployment failed for checkout"},
		{EventType: EventPreviewCreated, DisplayName: "Preview created", Description: "A pull request preview environment was created or updated.", SampleTitle: "Preview created for PR #42"},
		{EventType: EventPreviewArchived, DisplayName: "Preview archived", Description: "A pull request preview environment was archived after close or merge.", SampleTitle: "Preview archived for PR #42"},
	}
}

func BuildTestSendSample(channel ChannelConfig, event EventType) (TestSendSample, error) {
	if err := channel.Validate(); err != nil {
		return TestSendSample{}, err
	}
	entry, ok := catalogEntry(event)
	if !ok {
		return TestSendSample{}, fmt.Errorf("notification event %q is not supported", event)
	}
	sample := TestSendSample{
		ChannelID: channel.ID,
		EventType: event,
		Subject:   entry.SampleTitle,
		Text:      entry.Description,
		Fields: map[string]string{
			"event_type":   string(event),
			"workspace_id": channel.WorkspaceID,
			"channel_type": string(channel.Type),
		},
	}
	if channel.Type == ChannelSlackWebhook && channel.Slack.DefaultChannel != "" {
		sample.Fields["default_channel"] = channel.Slack.DefaultChannel
	}
	if channel.Type == ChannelSMTP {
		sample.Fields["from"] = channel.SMTP.From
	}
	return sample, nil
}

func BuildDeliveryPreview(channel ChannelConfig, subscription Subscription, event EventType, now time.Time) (DeliveryPreview, error) {
	if err := channel.Validate(); err != nil {
		return DeliveryPreview{}, err
	}
	if err := subscription.Validate(); err != nil {
		return DeliveryPreview{}, err
	}
	if subscription.WorkspaceID != channel.WorkspaceID || subscription.ChannelID != channel.ID {
		return DeliveryPreview{}, errors.New("subscription does not target notification channel")
	}
	if !validEvent(event) {
		return DeliveryPreview{}, fmt.Errorf("notification event %q is not supported", event)
	}
	if !containsEvent(subscription.EventTypes, event) {
		return DeliveryPreview{}, fmt.Errorf("subscription does not include event %q", event)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	preview := DeliveryPreview{
		ChannelID:     channel.ID,
		WorkspaceID:   channel.WorkspaceID,
		Provider:      channel.Type,
		EventType:     event,
		FailureCode:   failurecodes.NotificationDeliveryFailed,
		AuditActions:  []string{"notification.test_send", "notification.delivery.planned"},
		PayloadFields: payloadFields(channel.Type),
	}
	if !subscription.MutedUntil.IsZero() && now.Before(subscription.MutedUntil) {
		preview.Suppressed = true
		preview.AuditActions = []string{"notification.delivery.suppressed"}
	}
	return preview, nil
}

func (channel ChannelConfig) Validate() error {
	if strings.TrimSpace(channel.ID) == "" || strings.TrimSpace(channel.WorkspaceID) == "" {
		return errors.New("notification channel id and workspace_id are required")
	}
	if strings.TrimSpace(channel.DisplayName) == "" {
		return errors.New("notification channel display_name is required")
	}
	switch channel.Type {
	case ChannelSlackWebhook:
		if channel.Slack == nil || channel.SMTP != nil {
			return errors.New("slack webhook channel requires only slack config")
		}
		return channel.Slack.Validate()
	case ChannelSMTP:
		if channel.SMTP == nil || channel.Slack != nil {
			return errors.New("smtp channel requires only smtp config")
		}
		return channel.SMTP.Validate()
	default:
		return fmt.Errorf("notification channel type %q is not supported", channel.Type)
	}
}

func (config SlackConfig) Validate() error {
	if strings.TrimSpace(config.WebhookSecretRef) == "" {
		return errors.New("slack webhook_secret_ref is required")
	}
	return nil
}

func (config SMTPConfig) Validate() error {
	if strings.TrimSpace(config.Host) == "" {
		return errors.New("smtp host is required")
	}
	if config.Port <= 0 || config.Port > 65535 {
		return errors.New("smtp port must be between 1 and 65535")
	}
	if !config.TLS {
		return errors.New("smtp tls must be enabled")
	}
	if _, err := mail.ParseAddress(config.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if (config.UsernameSecretRef == "") != (config.PasswordSecretRef == "") {
		return errors.New("smtp username_secret_ref and password_secret_ref must be configured together")
	}
	return nil
}

func (subscription Subscription) Validate() error {
	if strings.TrimSpace(subscription.ID) == "" || strings.TrimSpace(subscription.WorkspaceID) == "" || strings.TrimSpace(subscription.ChannelID) == "" {
		return errors.New("notification subscription id, workspace_id, and channel_id are required")
	}
	if len(subscription.EventTypes) == 0 {
		return errors.New("notification subscription requires at least one event type")
	}
	seen := make(map[EventType]struct{}, len(subscription.EventTypes))
	for i, event := range subscription.EventTypes {
		if !validEvent(event) {
			return fmt.Errorf("event_types[%d]: unsupported notification event %q", i, event)
		}
		if _, ok := seen[event]; ok {
			return fmt.Errorf("event_types[%d]: duplicate notification event %q", i, event)
		}
		seen[event] = struct{}{}
	}
	return nil
}

func validEvent(event EventType) bool {
	_, ok := catalogEntry(event)
	return ok
}

func catalogEntry(event EventType) (CatalogEntry, bool) {
	for _, entry := range SubscriptionCatalog() {
		if entry.EventType == event {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

func containsEvent(events []EventType, want EventType) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func payloadFields(channelType ChannelType) []string {
	switch channelType {
	case ChannelSlackWebhook:
		return []string{"text", "blocks", "event_type", "workspace_id"}
	case ChannelSMTP:
		return []string{"to", "from", "subject", "text_body", "event_type", "workspace_id"}
	default:
		return nil
	}
}
