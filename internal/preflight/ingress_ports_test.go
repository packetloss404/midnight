package preflight

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSSLNTOutput = `State    Recv-Q  Send-Q  Local Address:Port  Peer Address:Port
LISTEN   0       4096    0.0.0.0:80          0.0.0.0:*
LISTEN   0       4096    [::]:443            [::]:*
LISTEN   0       128     127.0.0.1:22        0.0.0.0:*
`

func TestParseSSLNTOutput(t *testing.T) {
	t.Parallel()

	listeners, err := ParseSSLNTOutput(sampleSSLNTOutput)
	if err != nil {
		t.Fatalf("ParseSSLNTOutput() error = %v", err)
	}
	if len(listeners) != 3 {
		t.Fatalf("len(listeners) = %d, want 3", len(listeners))
	}
	tests := []struct {
		index   int
		port    int
		address string
		state   string
	}{
		{index: 0, port: 80, address: "0.0.0.0", state: "LISTEN"},
		{index: 1, port: 443, address: "[::]", state: "LISTEN"},
		{index: 2, port: 22, address: "127.0.0.1", state: "LISTEN"},
	}
	for _, tt := range tests {
		got := listeners[tt.index]
		if got.Port != tt.port || got.Address != tt.address || got.State != tt.state {
			t.Fatalf("listeners[%d] = %+v, want port=%d address=%s state=%s", tt.index, got, tt.port, tt.address, tt.state)
		}
	}
}

func TestParseSSLNTOutputAcceptsProtocolColumn(t *testing.T) {
	t.Parallel()

	listeners, err := ParseSSLNTOutput("tcp LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*\n")
	if err != nil {
		t.Fatalf("ParseSSLNTOutput() error = %v", err)
	}
	if len(listeners) != 1 || listeners[0].Protocol != "tcp" || listeners[0].Port != 80 {
		t.Fatalf("listeners = %+v, want one tcp listener on 80", listeners)
	}
}

func TestParseSSLNTOutputRejectsMalformed(t *testing.T) {
	t.Parallel()

	tests := []string{
		"LISTEN 0 4096 not-a-port-token",
		"LISTEN 0 abc 0.0.0.0:80 0.0.0.0:*",
		"LISTEN 0 4096 0.0.0.0:99999 0.0.0.0:*",
	}
	for _, input := range tests {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseSSLNTOutput(input); err == nil {
				t.Fatalf("ParseSSLNTOutput(%q) error = nil, want error", input)
			}
		})
	}
}

func TestReviewIngressPortBound(t *testing.T) {
	t.Parallel()

	listeners, err := ParseSSLNTOutput(sampleSSLNTOutput)
	if err != nil {
		t.Fatalf("ParseSSLNTOutput() error = %v", err)
	}
	review := ReviewIngressPort(80, listeners)
	if review.State != IngressPortBound {
		t.Fatalf("State = %q, want %q", review.State, IngressPortBound)
	}
	if len(review.Listeners) == 0 {
		t.Fatalf("Listeners = %+v, want at least one", review.Listeners)
	}
}

func TestReviewIngressPortFree(t *testing.T) {
	t.Parallel()

	review := ReviewIngressPort(8080, nil)
	if review.State != IngressPortFree {
		t.Fatalf("State = %q, want %q", review.State, IngressPortFree)
	}
}

func TestReviewIngressPortRejectsInvalidPort(t *testing.T) {
	t.Parallel()

	review := ReviewIngressPort(0, nil)
	if review.State != IngressPortFree || review.Reason == "" {
		t.Fatalf("review = %+v, want free with reason", review)
	}
}

func TestReviewPreflightIngressPorts(t *testing.T) {
	t.Parallel()

	listeners, err := ParseSSLNTOutput(sampleSSLNTOutput)
	if err != nil {
		t.Fatalf("ParseSSLNTOutput() error = %v", err)
	}
	reviews := ReviewPreflightIngressPorts(listeners)
	if len(reviews) != 2 {
		t.Fatalf("len(reviews) = %d, want 2", len(reviews))
	}
	for _, review := range reviews {
		if review.State != IngressPortBound {
			t.Fatalf("review = %+v, want bound", review)
		}
	}
}

func TestMarshalIngressPortReviewJSONRoundTrip(t *testing.T) {
	t.Parallel()

	review := ReviewIngressPort(80, nil)
	data, err := MarshalIngressPortReviewJSON(review)
	if err != nil {
		t.Fatalf("MarshalIngressPortReviewJSON() error = %v", err)
	}
	var decoded IngressPortReview
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(review) error = %v", err)
	}
	if decoded.Port != 80 || decoded.State != IngressPortFree {
		t.Fatalf("decoded = %+v, want free 80", decoded)
	}
}

func TestCheckIngressPortsFromPathFromFakeReader(t *testing.T) {
	t.Parallel()

	reader := fakeFileReader{
		"ss.txt": []byte(sampleSSLNTOutput),
	}
	source := HostSource{Reader: reader}
	result := CheckIngressPortsFromPath(context.Background(), source, "ss.txt")
	if result.Status != StatusFail {
		t.Fatalf("Status = %q, want fail because port 80 is bound", result.Status)
	}
}

func TestCheckIngressPortsFromPathPasses(t *testing.T) {
	t.Parallel()

	reader := fakeFileReader{
		"empty.txt": []byte("State Recv-Q Send-Q Local Address:Port Peer Address:Port\nLISTEN 0 128 127.0.0.1:22 0.0.0.0:*\n"),
	}
	source := HostSource{Reader: reader}
	result := CheckIngressPortsFromPath(context.Background(), source, "empty.txt")
	if result.Status != StatusPass {
		t.Fatalf("Status = %q, want pass", result.Status)
	}
}

func TestCheckIngressPortsFromPathRequiresInputs(t *testing.T) {
	t.Parallel()

	if got := CheckIngressPortsFromPath(context.Background(), HostSource{}, ""); got.Status != StatusSkip {
		t.Fatalf("Status = %q, want skip when path empty", got.Status)
	}
	if got := CheckIngressPortsFromPath(context.Background(), HostSource{}, "x"); got.Status != StatusFail {
		t.Fatalf("Status = %q, want fail when reader missing", got.Status)
	}
}

func TestParseSSListenSnapshot(t *testing.T) {
	t.Parallel()

	snapshot, err := ParseSSListenSnapshot(sampleSSLNTOutput)
	if err != nil {
		t.Fatalf("ParseSSListenSnapshot() error = %v", err)
	}
	if len(snapshot.Listeners) != 3 {
		t.Fatalf("len(Listeners) = %d, want 3", len(snapshot.Listeners))
	}
	wantPorts := []int{22, 80, 443}
	if len(snapshot.OccupiedPorts) != len(wantPorts) {
		t.Fatalf("OccupiedPorts = %v, want %v", snapshot.OccupiedPorts, wantPorts)
	}
	for index, want := range wantPorts {
		if snapshot.OccupiedPorts[index] != want {
			t.Fatalf("OccupiedPorts[%d] = %d, want %d", index, snapshot.OccupiedPorts[index], want)
		}
	}
	for _, port := range wantPorts {
		if !snapshot.OccupiedPort(port) {
			t.Fatalf("OccupiedPort(%d) = false, want true", port)
		}
	}
	if snapshot.OccupiedPort(8080) {
		t.Fatal("OccupiedPort(8080) = true, want false")
	}
}

func TestParseSSListenSnapshotDeduplicatesPorts(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		"State Recv-Q Send-Q Local Address:Port Peer Address:Port",
		"LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*",
		"LISTEN 0 4096 [::]:80 [::]:*",
	}, "\n")
	snapshot, err := ParseSSListenSnapshot(input)
	if err != nil {
		t.Fatalf("ParseSSListenSnapshot() error = %v", err)
	}
	if len(snapshot.OccupiedPorts) != 1 || snapshot.OccupiedPorts[0] != 80 {
		t.Fatalf("OccupiedPorts = %v, want [80]", snapshot.OccupiedPorts)
	}
}

func TestParseSSListenSnapshotRejectsMalformed(t *testing.T) {
	t.Parallel()

	if _, err := ParseSSListenSnapshot("LISTEN 0 4096 not-a-port-token"); err == nil {
		t.Fatal("ParseSSListenSnapshot(bad) error = nil, want parse error")
	}
}

func TestCheckIngressPortsFromPathFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "ss.txt")
	source := HostSource{Reader: OSFileReader{}}
	if got := CheckIngressPortsFromPath(context.Background(), source, path); got.Status != StatusFail {
		t.Fatalf("Status = %q, want fail when file missing", got.Status)
	}
}
