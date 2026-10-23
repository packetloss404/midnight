package observability

import "testing"

func TestPlanCollectorPipelineDefaultOff(t *testing.T) {
	readiness, err := PlanCollectorPipeline(CollectorPipelineConfig{})
	if err != nil {
		t.Fatalf("PlanCollectorPipeline: %v", err)
	}
	if readiness.Status != "disabled" || readiness.SchemaVersion != CollectorPipelineSchemaVersion {
		t.Fatalf("readiness = %#v", readiness)
	}
}

func TestPlanCollectorPipelineReady(t *testing.T) {
	readiness, err := PlanCollectorPipeline(validCollectorPipelineConfig())
	if err != nil {
		t.Fatalf("PlanCollectorPipeline: %v", err)
	}
	if readiness.Status != "ready" || readiness.ReceiverCount != 1 || readiness.ProcessorCount != 2 || readiness.ExporterCount != 1 || readiness.RedactionPolicy != "midnight-default" {
		t.Fatalf("readiness = %#v", readiness)
	}
}

func TestCollectorPipelineRejectsMissingRedaction(t *testing.T) {
	config := validCollectorPipelineConfig()
	config.Processors = []CollectorProcessor{{Name: "batch", Type: CollectorProcessorBatch}}
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing redaction processor to be rejected")
	}
}

func TestCollectorPipelineRejectsUnsafeExporterEndpoint(t *testing.T) {
	config := validCollectorPipelineConfig()
	config.Exporters[0].Endpoint = "http://collector.example.com/v1/traces"
	if err := config.Validate(); err == nil {
		t.Fatal("expected non-https exporter endpoint to be rejected")
	}
}

func validCollectorPipelineConfig() CollectorPipelineConfig {
	return CollectorPipelineConfig{
		Enabled: true,
		Receivers: []CollectorReceiver{{
			Name:     "otlp",
			Protocol: CollectorProtocolOTLPHTTP,
			Endpoint: "0.0.0.0:4318",
		}},
		Processors: []CollectorProcessor{
			{Name: "redact", Type: CollectorProcessorRedaction},
			{Name: "batch", Type: CollectorProcessorBatch},
		},
		Exporters: []CollectorExporter{{
			Name:     "managed-collector",
			Protocol: CollectorProtocolOTLPHTTP,
			Endpoint: "https://collector.example.com/v1/traces",
		}},
		RedactionPolicy: "midnight-default",
	}
}
