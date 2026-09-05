package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateWebSocketRequest(t *testing.T) {
	t.Parallel()

	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	request := httptest.NewRequest(http.MethodGet, "http://example.test/ws", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "WebSocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", key)

	got, err := validateWebSocketRequest(request)
	if err != nil {
		t.Fatalf("validateWebSocketRequest: %v", err)
	}
	if got != key {
		t.Fatalf("key = %q, want %q", got, key)
	}

	request.Header.Del("Connection")
	if _, err := validateWebSocketRequest(request); err == nil {
		t.Fatal("missing Connection token unexpectedly accepted")
	}
}

func TestDecodeWebSocketEarlyData(t *testing.T) {
	t.Parallel()

	payload := []byte{0xfb, 0xff, 0xef, 0x00, 0x01, 0x02, 0x03}
	protocol := base64.StdEncoding.EncodeToString(payload)

	got, ok, err := decodeWebSocketEarlyData(protocol, 1024)
	if err != nil {
		t.Fatalf("decodeWebSocketEarlyData: %v", err)
	}
	if !ok {
		t.Fatal("valid early data was not detected")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded payload = %x, want %x", got, payload)
	}

	if got, ok, err := decodeWebSocketEarlyData("not a valid websocket protocol!", 1024); err != nil || ok || got != nil {
		t.Fatalf("invalid protocol = %x, %v, %v; want nil, false, nil", got, ok, err)
	}

	if _, _, err := decodeWebSocketEarlyData(protocol, 4); !errors.Is(err, errMessageTooBig) {
		t.Fatalf("oversized early data error = %v, want %v", err, errMessageTooBig)
	}
}

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	server := &proxyServer{wsPath: "/ws", maxMessageBytes: 1024}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/healthz", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}
