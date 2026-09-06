package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPulsePathMeta(t *testing.T) {
	t.Parallel()

	base := "/assets/api/v1"
	tests := []struct {
		path      string
		sessionID string
		seq       uint64
		hasSeq    bool
		wantErr   bool
	}{
		{path: base},
		{path: base + "/"},
		{path: base + "/abc_DEF-123", sessionID: "abc_DEF-123"},
		{path: base + "/abc_DEF-123/0", sessionID: "abc_DEF-123", seq: 0, hasSeq: true},
		{path: base + "/abc_DEF-123/42", sessionID: "abc_DEF-123", seq: 42, hasSeq: true},
		{path: base + "/abc/1/extra", wantErr: true},
		{path: base + "/abc/not-a-seq", wantErr: true},
		{path: "/other", wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			sessionID, seq, hasSeq, err := parsePulsePathMeta(test.path, base)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePulsePathMeta: %v", err)
			}
			if sessionID != test.sessionID || seq != test.seq || hasSeq != test.hasSeq {
				t.Fatalf("got session=%q seq=%d hasSeq=%v", sessionID, seq, hasSeq)
			}
		})
	}

	if isPulseRequestPath("/healthz", "") {
		t.Fatal("empty Pulse path must never match")
	}
}

func TestPulseUploadQueueReordersPackets(t *testing.T) {
	t.Parallel()

	queue := newPulseUploadQueue()
	defer queue.Close()

	if err := queue.addPacket(1, []byte("world")); err != nil {
		t.Fatal(err)
	}
	if err := queue.addPacket(0, []byte("hello ")); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len("hello world"))
	if _, err := io.ReadFull(queue, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestPulseUploadQueueIgnoresReplayDuringPartialRead(t *testing.T) {
	t.Parallel()

	queue := newPulseUploadQueue()
	defer queue.Close()

	if err := queue.addPacket(0, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}

	first := make([]byte, 3)
	if _, err := io.ReadFull(queue, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "abc" {
		t.Fatalf("first read = %q", first)
	}

	if err := queue.addPacket(0, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := queue.addPacket(1, []byte("ghi")); err != nil {
		t.Fatal(err)
	}

	rest := make([]byte, 6)
	if _, err := io.ReadFull(queue, rest); err != nil {
		t.Fatal(err)
	}
	if string(rest) != "defghi" {
		t.Fatalf("remaining stream = %q", rest)
	}

	queue.mu.Lock()
	buffered := len(queue.packets)
	queue.mu.Unlock()
	if buffered != 0 {
		t.Fatalf("replayed packet remained buffered: %d", buffered)
	}
}

func TestPulseUploadQueueCloseStopsBufferedData(t *testing.T) {
	t.Parallel()

	queue := newPulseUploadQueue()
	if err := queue.addPacket(0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 4)
	n, err := queue.Read(buffer)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read after close = %d, %v; want 0, EOF", n, err)
	}
}

func TestPulseUploadModeConflict(t *testing.T) {
	t.Parallel()

	queue := newPulseUploadQueue()
	defer queue.Close()
	if err := queue.addPacket(0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := queue.setStream(io.NopCloser(bytes.NewReader(nil))); !errors.Is(err, errPulseModeConflict) {
		t.Fatalf("setStream error = %v", err)
	}
}

func TestPulseStreamUpKeepaliveRanges(t *testing.T) {
	t.Parallel()

	for range 100 {
		delay := pulseStreamUpKeepaliveDelay()
		if delay < 20*time.Second || delay > 80*time.Second {
			t.Fatalf("keepalive delay = %s", delay)
		}

		padding := pulseStreamUpPadding()
		if len(padding) < 100 || len(padding) > 1000 {
			t.Fatalf("padding length = %d", len(padding))
		}
		if !bytes.Equal(padding, bytes.Repeat([]byte{'X'}, len(padding))) {
			t.Fatal("keepalive padding contains non-X data")
		}
	}
}

func TestPulsePacketRequest(t *testing.T) {
	t.Parallel()

	server := &proxyServer{
		pulsePath:           "/assets/api/v1",
		pulseMaxPacketBytes: 1024,
		pulseSessions:       make(map[string]*pulseSession),
	}
	defer server.closePulseSessions()

	request := httptest.NewRequest(http.MethodPost, "http://example.test/assets/api/v1/session123/0", bytes.NewReader([]byte("hello")))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}

	server.pulseMu.Lock()
	session := server.pulseSessions["session123"]
	server.pulseMu.Unlock()
	if session == nil {
		t.Fatal("session was not created")
	}

	got := make([]byte, 5)
	if _, err := io.ReadFull(session.upload, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("payload = %q", got)
	}
}

func TestPulseStreamOneRoute(t *testing.T) {
	t.Parallel()

	server := &proxyServer{
		pulsePath:           "/assets/api/v1",
		pulseMaxPacketBytes: 1024,
		pulseSessions:       make(map[string]*pulseSession),
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.test/assets/api/v1/", bytes.NewReader(nil))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
