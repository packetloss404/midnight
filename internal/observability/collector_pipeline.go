package observability

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const CollectorPipelineSchemaVersion = "midnight.collector.pipeline.v1"

type CollectorProtocol string

const (
	CollectorProtocolOTLPHTTP CollectorProtocol = "otlp-http"
	CollectorProtocolOTLPGRPC CollectorProtocol = "otlp-grpc"
)

type CollectorProcessorType string

const (
	CollectorProcessorRedaction CollectorProcessorType = "redaction"
	CollectorProcessorBatch     CollectorProcessorType = "batch"
)

type CollectorPipelineConfig struct {
	Enabled         bool                 `json:"enabled"`
	Receivers       []CollectorReceiver  `json:"receivers"`
	Processors      []CollectorProcessor `json:"processors"`
	Exporters       []CollectorExporter  `json:"exporters"`
	RedactionPolicy string               `json:"redaction_policy"`
}

type CollectorReceiver struct {
	Name     string            `json:"name"`
	Protocol CollectorProtocol `json:"protocol"`
	Endpoint string            `json:"endpoint"`
}

type CollectorProcessor struct {
	Name string                 `json:"name"`
	Type CollectorProcessorType `json:"type"`
}

type CollectorExporter struct {
	Name     string            `json:"name"`
	Protocol CollectorProtocol `json:"protocol"`
	Endpoint string            `json:"endpoint"`
}

type CollectorPipelineReadiness struct {
	SchemaVersion   string `json:"schema_version"`
	Status          string `json:"status"`
	ReceiverCount   int    `json:"receiver_count"`
	ProcessorCount  int    `json:"processor_count"`
	ExporterCount   int    `json:"exporter_count"`
	RedactionPolicy string `json:"redaction_policy,omitempty"`
}

func PlanCollectorPipeline(config CollectorPipelineConfig) (CollectorPipelineReadiness, error) {
	readiness := CollectorPipelineReadiness{SchemaVersion: CollectorPipelineSchemaVersion, Status: "disabled"}
	if !config.Enabled {
		return readiness, nil
	}
	if err := config.Validate(); err != nil {
		readiness.Status = "blocked"
		return readiness, err
	}
	readiness.Status = "ready"
	readiness.ReceiverCount = len(config.Receivers)
	readiness.ProcessorCount = len(config.Processors)
	readiness.ExporterCount = len(config.Exporters)
	readiness.RedactionPolicy = strings.TrimSpace(config.RedactionPolicy)
	return readiness, nil
}

func (config CollectorPipelineConfig) Validate() error {
	if !config.Enabled {
		return nil
	}
	if len(config.Receivers) == 0 {
		return errors.New("collector pipeline requires at least one receiver")
	}
	if len(config.Exporters) == 0 {
		return errors.New("collector pipeline requires at least one exporter")
	}
	if strings.TrimSpace(config.RedactionPolicy) == "" {
		return errors.New("collector pipeline requires a redaction policy")
	}
	for i, receiver := range config.Receivers {
		if err := receiver.Validate(); err != nil {
			return fmt.Errorf("receivers[%d]: %w", i, err)
		}
	}
	redactionProcessor := false
	for i, processor := range config.Processors {
		if err := processor.Validate(); err != nil {
			return fmt.Errorf("processors[%d]: %w", i, err)
		}
		if processor.Type == CollectorProcessorRedaction {
			redactionProcessor = true
		}
	}
	if !redactionProcessor {
		return errors.New("collector pipeline requires a redaction processor before export")
	}
	for i, exporter := range config.Exporters {
		if err := exporter.Validate(); err != nil {
			return fmt.Errorf("exporters[%d]: %w", i, err)
		}
	}
	return nil
}

func (receiver CollectorReceiver) Validate() error {
	if strings.TrimSpace(receiver.Name) == "" {
		return errors.New("name is required")
	}
	if !validCollectorProtocol(receiver.Protocol) {
		return fmt.Errorf("unsupported protocol %q", receiver.Protocol)
	}
	if strings.TrimSpace(receiver.Endpoint) == "" {
		return errors.New("endpoint is required")
	}
	return nil
}

func (processor CollectorProcessor) Validate() error {
	if strings.TrimSpace(processor.Name) == "" {
		return errors.New("name is required")
	}
	switch processor.Type {
	case CollectorProcessorRedaction, CollectorProcessorBatch:
		return nil
	default:
		return fmt.Errorf("unsupported processor type %q", processor.Type)
	}
}

func (exporter CollectorExporter) Validate() error {
	if strings.TrimSpace(exporter.Name) == "" {
		return errors.New("name is required")
	}
	if !validCollectorProtocol(exporter.Protocol) {
		return fmt.Errorf("unsupported protocol %q", exporter.Protocol)
	}
	endpoint, err := url.Parse(strings.TrimSpace(exporter.Endpoint))
	if err != nil {
		return err
	}
	if endpoint.Scheme != "https" || endpoint.Host == "" {
		return errors.New("endpoint must be an absolute https URL")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("endpoint must not include query or fragment")
	}
	return nil
}

func validCollectorProtocol(protocol CollectorProtocol) bool {
	switch protocol {
	case CollectorProtocolOTLPHTTP, CollectorProtocolOTLPGRPC:
		return true
	default:
		return false
	}
}
