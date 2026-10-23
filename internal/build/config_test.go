package build

import (
	"testing"
	"time"
)

func TestWorkerConfigValidateAcceptsSafeLimits(t *testing.T) {
	config := WorkerConfig{
		Timeout:             30 * time.Minute,
		MaxConcurrency:      2,
		MinFreeDiskBytes:    10 << 30,
		MaxDiskUsagePercent: 85,
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestWorkerConfigValidateRejectsInvalidLimits(t *testing.T) {
	tests := map[string]WorkerConfig{
		"timeout":      {MaxConcurrency: 1, MaxDiskUsagePercent: 80},
		"concurrency":  {Timeout: time.Minute, MaxDiskUsagePercent: 80},
		"free disk":    {Timeout: time.Minute, MaxConcurrency: 1, MinFreeDiskBytes: -1, MaxDiskUsagePercent: 80},
		"disk percent": {Timeout: time.Minute, MaxConcurrency: 1, MaxDiskUsagePercent: 101},
	}

	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid config")
			}
		})
	}
}
