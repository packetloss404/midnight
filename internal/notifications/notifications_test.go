package notifications

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestBuildDeliveryPreviewForSlack(t *testing.T) {
	channel := validSlackChannel()
	subscription := Subscription{ID: "sub_1", WorkspaceID: "wrk_acme", ChannelID: channel.ID, EventTypes: []EventType{EventDeploymentFailed}}
	preview, err := BuildDeliveryPreview(channel, subscription, EventDeploymentFailed, time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildDeliveryPreview: %v", err)
	}
	if preview.Provider != ChannelSlackWebhook || preview.FailureCode != failurecodes.NotificationDeliveryFailed || preview.Suppressed {
		t.Fatalf("preview = %#v", preview)
	}
	if len(preview.AuditActions) != 2 || preview.AuditActions[0] != "notification.test_send" {
		t.Fatalf("audit actions = %#v", preview.AuditActions)
	}
}

func TestBuildDeliveryPreviewSuppressesMutedSubscription(t *testing.T) {
	channel := validSlackChannel()
	subscription := Subscription{ID: "sub_1", WorkspaceID: "wrk_acme", ChannelID: channel.ID, EventTypes: []EventType{EventPreviewCreated}, MutedUntil: time.Date(2026, 4, 26, 13, 0, 0, 0, time.UTC)}
	preview, err := BuildDeliveryPreview(channel, subscription, EventPreviewCreated, time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildDeliveryPreview: %v", err)
	}
	if !preview.Suppressed || len(preview.AuditActions) != 1 || preview.AuditActions[0] != "notification.delivery.suppressed" {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestSMTPChannelRequiresTLSAndValidFrom(t *testing.T) {
	channel := ChannelConfig{ID: "email-prod", WorkspaceID: "wrk_acme", Type: ChannelSMTP, DisplayName: "Email", SMTP: &SMTPConfig{Host: "smtp.example.com", Port: 587, TLS: true, From: "Midnight <midnight@example.com>"}}
	if err := channel.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	channel.SMTP.TLS = false
	if err := channel.Validate(); err == nil {
		t.Fatal("expected non-TLS SMTP config to be rejected")
	}
	channel.SMTP.TLS = true
	channel.SMTP.From = "not an address"
	if err := channel.Validate(); err == nil {
		t.Fatal("expected invalid From address to be rejected")
	}
}

func TestSubscriptionRejectsDuplicateEvents(t *testing.T) {
	err := (Subscription{ID: "sub_1", WorkspaceID: "wrk_acme", ChannelID: "slack", EventTypes: []EventType{EventDeploymentFailed, EventDeploymentFailed}}).Validate()
	if err == nil {
		t.Fatal("expected duplicate event type to be rejected")
	}
}

func TestSubscriptionCatalogCoversSupportedEvents(t *testing.T) {
	catalog := SubscriptionCatalog()
	if len(catalog) != 4 {
		t.Fatalf("catalog len = %d, want 4", len(catalog))
	}
	for _, event := range []EventType{EventDeploymentSucceeded, EventDeploymentFailed, EventPreviewCreated, EventPreviewArchived} {
		if _, ok := catalogEntry(event); !ok {
			t.Fatalf("catalog missing %q", event)
		}
	}
}

func TestBuildTestSendSampleDoesNotRequireDeliveryTarget(t *testing.T) {
	sample, err := BuildTestSendSample(validSlackChannel(), EventPreviewCreated)
	if err != nil {
		t.Fatalf("BuildTestSendSample: %v", err)
	}
	if sample.ChannelID != "slack-prod" || sample.Fields["default_channel"] != "#deploys" || sample.Fields["event_type"] != string(EventPreviewCreated) {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestBuildTestSendSampleRejectsUnsupportedEvent(t *testing.T) {
	if _, err := BuildTestSendSample(validSlackChannel(), EventType("workspace.deleted")); err == nil {
		t.Fatal("expected unsupported event to be rejected")
	}
}

func validSlackChannel() ChannelConfig {
	return ChannelConfig{ID: "slack-prod", WorkspaceID: "wrk_acme", Type: ChannelSlackWebhook, DisplayName: "Deployments", Slack: &SlackConfig{WebhookSecretRef: "secret_slack_webhook", DefaultChannel: "#deploys"}}
}
