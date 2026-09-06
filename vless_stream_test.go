package main

import (
	"bytes"
	"io"
	"testing"
)

func TestReadVLESSRequestKeepsPayloadInStream(t *testing.T) {
	t.Parallel()

	uuid, err := parseUUID("00112233-4455-6677-8899-aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}

	domain := "example.com"
	message := make([]byte, 0, 64)
	message = append(message, 0)
	message = append(message, uuid[:]...)
	message = append(message, 0)
	message = append(message, commandTCP)
	message = append(message, 0x01, 0xBB)
	message = append(message, 2, byte(len(domain)))
	message = append(message, domain...)
	message = append(message, "hello"...)

	reader := bytes.NewReader(message)
	request, err := readVLESSRequest(reader, uuid)
	if err != nil {
		t.Fatalf("readVLESSRequest: %v", err)
	}
	if request.command != commandTCP || request.address != domain || request.port != 443 {
		t.Fatalf("unexpected request: %+v", request)
	}
	if len(request.payload) != 0 {
		t.Fatalf("stream parser unexpectedly consumed payload: %q", request.payload)
	}

	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "hello" {
		t.Fatalf("remaining payload = %q", payload)
	}
}
