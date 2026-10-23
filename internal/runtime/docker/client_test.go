package docker

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockerclient "github.com/moby/moby/client"
)

func TestRealEngineClientPingNegotiatesDaemonAPIVersion(t *testing.T) {
	for _, version := range []string{"1.42", MinEngineAPIVersion} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_ping" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("API-Version", version)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client, err := dockerclient.NewClientWithOpts(
				dockerclient.WithHost(server.URL),
				dockerclient.WithHTTPClient(server.Client()),
				dockerclient.WithAPIVersionNegotiation(),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			engine := &realEngineClient{cli: client}
			if err := engine.Ping(context.Background()); err != nil {
				t.Fatalf("Ping: %v", err)
			}
			if got := engine.NegotiatedAPIVersion(); got != version {
				t.Fatalf("negotiated API version = %q, want %q", got, version)
			}

			floorErr := EnforceEngineAPIFloor(engine.NegotiatedAPIVersion())
			if version == MinEngineAPIVersion && floorErr != nil {
				t.Fatalf("minimum supported API version was rejected: %v", floorErr)
			}
			if version != MinEngineAPIVersion && floorErr == nil {
				t.Fatal("unsupported API version was accepted")
			}
		})
	}
}

func TestDemultiplexDockerStreamStripsFraming(t *testing.T) {
	frame := func(stream byte, payload string) []byte {
		header := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
		return append(header, []byte(payload)...)
	}
	raw := append(frame(1, "starting\n"), frame(2, "FATAL: boom\n")...)
	if got := string(demultiplexDockerStream(raw)); got != "starting\nFATAL: boom\n" {
		t.Fatalf("demultiplexed = %q, want both frames joined without headers", got)
	}
}

func TestDemultiplexDockerStreamLeavesTTYOutputAlone(t *testing.T) {
	raw := []byte("plain tty output\nsecond line\n")
	if got := string(demultiplexDockerStream(raw)); got != string(raw) {
		t.Fatalf("TTY output = %q, want it returned untouched", got)
	}
}

func TestTailLogLinesKeepsLastLinesAndTruncatesLongOnes(t *testing.T) {
	payload := []byte("one\n\ntwo\r\nthree\n" + strings.Repeat("x", maxLogTailLineBytes+50) + "\n")
	lines := tailLogLines(payload, 3)
	if len(lines) != 3 {
		t.Fatalf("tailLogLines returned %d lines, want the last 3: %v", len(lines), lines)
	}
	if lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("tailLogLines dropped or reordered lines: %v", lines[:2])
	}
	if len([]rune(lines[2])) != maxLogTailLineBytes+1 {
		t.Fatalf("long line was not truncated to the cap plus an ellipsis: %d runes", len([]rune(lines[2])))
	}
}

func TestTailLogLinesReturnsNothingForBlankOutput(t *testing.T) {
	if got := tailLogLines([]byte("\n  \n\n"), 5); got != nil {
		t.Fatalf("blank output produced %v, want no lines", got)
	}
}
