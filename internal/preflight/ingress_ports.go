package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IngressPortBindingState is the offline classification of a port binding.
type IngressPortBindingState string

const (
	// IngressPortFree indicates the port is not present in the parsed listener output.
	IngressPortFree IngressPortBindingState = "free"
	// IngressPortBound indicates the port is bound to one or more listener entries.
	IngressPortBound IngressPortBindingState = "bound"
)

// SocketListener describes one parsed listener row from `ss -ltn`-style output.
type SocketListener struct {
	Protocol     string `json:"protocol,omitempty"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	State        string `json:"state,omitempty"`
	RecvQ        int    `json:"recv_q,omitempty"`
	SendQ        int    `json:"send_q,omitempty"`
	PeerAddress  string `json:"peer_address,omitempty"`
	BoundProcess string `json:"bound_process,omitempty"`
	OriginalLine string `json:"-"`
}

// IngressPortReview is a JSON-friendly summary used by installer wiring.
type IngressPortReview struct {
	Port      int                     `json:"port"`
	State     IngressPortBindingState `json:"state"`
	Listeners []SocketListener        `json:"listeners,omitempty"`
	Reason    string                  `json:"reason"`
}

// PreflightIngressPorts is the canonical Phase 0 ingress port set required by
// preflight-time port-bind checks (HTTP for ACME-01 challenge, HTTPS).
func PreflightIngressPorts() []int {
	return []int{80, 443}
}

// ParseSSLNTOutput parses `ss -ltn`-style output supplied by tests or capture
// fixtures. It tolerates the optional first header row and `Process=...`
// columns that newer iproute2 versions emit when invoked with `-p`.
//
// Example input:
//
//	State    Recv-Q  Send-Q  Local Address:Port  Peer Address:Port
//	LISTEN   0       4096    0.0.0.0:80          0.0.0.0:*
//	LISTEN   0       4096    [::]:443            [::]:*
func ParseSSLNTOutput(text string) ([]SocketListener, error) {
	var listeners []SocketListener
	for index, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if isSSLNTHeader(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("ss line %d %q has fewer than 4 fields", index+1, line)
		}
		// Detect optional protocol column emitted by `ss -ltnp -t -u`.
		offset := 0
		protocol := ""
		switch strings.ToLower(fields[0]) {
		case "tcp", "udp":
			protocol = strings.ToLower(fields[0])
			offset = 1
		}
		if len(fields) < offset+4 {
			return nil, fmt.Errorf("ss line %d %q is incomplete", index+1, line)
		}

		state := strings.ToUpper(fields[offset])
		recvQ, err := strconv.Atoi(fields[offset+1])
		if err != nil {
			return nil, fmt.Errorf("ss line %d recv-q %q: %w", index+1, fields[offset+1], err)
		}
		sendQ, err := strconv.Atoi(fields[offset+2])
		if err != nil {
			return nil, fmt.Errorf("ss line %d send-q %q: %w", index+1, fields[offset+2], err)
		}

		address, port, err := splitSSAddrPort(fields[offset+3])
		if err != nil {
			return nil, fmt.Errorf("ss line %d local %q: %w", index+1, fields[offset+3], err)
		}

		var peer string
		if len(fields) >= offset+5 {
			peer = fields[offset+4]
		}
		boundProcess := ""
		for _, extra := range fields[offset+5:] {
			if strings.HasPrefix(extra, "users:") || strings.HasPrefix(extra, "Process=") {
				boundProcess = extra
				break
			}
		}

		listeners = append(listeners, SocketListener{
			Protocol:     protocol,
			Address:      address,
			Port:         port,
			State:        state,
			RecvQ:        recvQ,
			SendQ:        sendQ,
			PeerAddress:  peer,
			BoundProcess: boundProcess,
			OriginalLine: line,
		})
	}
	return listeners, nil
}

// ReviewIngressPort scans listeners for any binding on port and returns a
// JSON-friendly summary. It does not query the host.
func ReviewIngressPort(port int, listeners []SocketListener) IngressPortReview {
	if port <= 0 || port > 65535 {
		return IngressPortReview{Port: port, State: IngressPortFree, Reason: fmt.Sprintf("port %d is outside the valid TCP range", port)}
	}
	var matching []SocketListener
	for _, listener := range listeners {
		if listener.Port == port && (listener.State == "" || listener.State == "LISTEN") {
			matching = append(matching, listener)
		}
	}
	if len(matching) == 0 {
		return IngressPortReview{Port: port, State: IngressPortFree, Reason: fmt.Sprintf("port %d is not bound by any listener in the captured output", port)}
	}
	sort.SliceStable(matching, func(left, right int) bool {
		if matching[left].Address != matching[right].Address {
			return matching[left].Address < matching[right].Address
		}
		return matching[left].OriginalLine < matching[right].OriginalLine
	})
	return IngressPortReview{
		Port:      port,
		State:     IngressPortBound,
		Listeners: matching,
		Reason:    fmt.Sprintf("port %d is already bound by %d listener(s)", port, len(matching)),
	}
}

// ReviewPreflightIngressPorts returns one review per Phase 0 ingress port.
func ReviewPreflightIngressPorts(listeners []SocketListener) []IngressPortReview {
	ports := PreflightIngressPorts()
	reviews := make([]IngressPortReview, 0, len(ports))
	for _, port := range ports {
		reviews = append(reviews, ReviewIngressPort(port, listeners))
	}
	return reviews
}

// SSListenSnapshot is the parsed result of an offline `ss -ltn`-style capture.
// It carries the deterministic listener list plus a sorted list of occupied
// ports so callers can quickly answer "which TCP ports are bound?" without
// re-walking listeners.
type SSListenSnapshot struct {
	Listeners     []SocketListener `json:"listeners"`
	OccupiedPorts []int            `json:"occupied_ports"`
}

// ParseSSListenSnapshot parses `ss -ltn`-style output and returns a stable
// snapshot containing both the listener entries and the sorted unique set of
// ports observed in LISTEN state. It does not perform any network or OS calls.
//
// The parser tolerates the optional header row and the optional protocol/process
// columns that newer iproute2 versions emit; ports observed in non-LISTEN
// states (e.g. ESTABLISHED) are still included in OccupiedPorts.
func ParseSSListenSnapshot(text string) (SSListenSnapshot, error) {
	listeners, err := ParseSSLNTOutput(text)
	if err != nil {
		return SSListenSnapshot{}, err
	}
	seen := make(map[int]struct{}, len(listeners))
	for _, listener := range listeners {
		if listener.Port <= 0 {
			continue
		}
		seen[listener.Port] = struct{}{}
	}
	occupied := make([]int, 0, len(seen))
	for port := range seen {
		occupied = append(occupied, port)
	}
	sort.Ints(occupied)
	return SSListenSnapshot{Listeners: listeners, OccupiedPorts: occupied}, nil
}

// OccupiedPort reports whether port appears in the snapshot's OccupiedPorts list.
func (s SSListenSnapshot) OccupiedPort(port int) bool {
	for _, occupied := range s.OccupiedPorts {
		if occupied == port {
			return true
		}
	}
	return false
}

// MarshalIngressPortReviewJSON emits stable per-port review JSON.
func MarshalIngressPortReviewJSON(review IngressPortReview) ([]byte, error) {
	return json.MarshalIndent(review, "", "  ")
}

// CheckIngressPortsFromPath runs the preflight ingress-port check against a
// caller-supplied fixture file containing `ss -ltn` output. It is offline by
// default; live wiring must opt-in through the OS command runner bridge.
func CheckIngressPortsFromPath(_ context.Context, source HostSource, path string) Result {
	if strings.TrimSpace(path) == "" {
		return Result{ID: "PRE-INGRESS-PORTS-001", Status: StatusSkip, Message: "ss listener fixture path not provided"}
	}
	if source.Reader == nil {
		return Result{ID: "PRE-INGRESS-PORTS-001", Status: StatusFail, Err: fmt.Errorf("ss listener file reader is required")}
	}
	data, err := source.Reader.ReadFile(path)
	if err != nil {
		return Result{ID: "PRE-INGRESS-PORTS-001", Status: StatusFail, Err: err}
	}
	listeners, err := ParseSSLNTOutput(string(data))
	if err != nil {
		return Result{ID: "PRE-INGRESS-PORTS-001", Status: StatusFail, Err: err}
	}
	reviews := ReviewPreflightIngressPorts(listeners)
	for _, review := range reviews {
		if review.State == IngressPortBound {
			return Result{
				ID:      "PRE-INGRESS-PORTS-001",
				Status:  StatusFail,
				Message: review.Reason,
			}
		}
	}
	return Result{ID: "PRE-INGRESS-PORTS-001", Status: StatusPass, Message: "Phase 0 ingress ports are not pre-bound"}
}

func isSSLNTHeader(line string) bool {
	upper := strings.ToUpper(line)
	if strings.HasPrefix(upper, "STATE") || strings.HasPrefix(upper, "NETID") {
		return true
	}
	return false
}

func splitSSAddrPort(token string) (string, int, error) {
	idx := strings.LastIndex(token, ":")
	if idx <= 0 || idx == len(token)-1 {
		return "", 0, fmt.Errorf("address:port token %q is invalid", token)
	}
	address := token[:idx]
	portText := token[idx+1:]
	if portText == "*" {
		return address, 0, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("port %q: %w", portText, err)
	}
	if port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("port %d is out of range", port)
	}
	return address, port, nil
}
