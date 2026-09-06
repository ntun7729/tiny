package main

import "testing"

func TestPulseConfigParsers(t *testing.T) {
	t.Parallel()

	if got, err := parsePulsePath(""); err != nil || got != defaultPulsePath {
		t.Fatalf("default pulse path = %q, %v", got, err)
	}
	if got, err := parsePulsePath("api/tunnel/"); err != nil || got != "/api/tunnel" {
		t.Fatalf("pulse path = %q, %v", got, err)
	}
	for _, value := range []string{"/", "/healthz", "/a/../b", "/a?x=1"} {
		if _, err := parsePulsePath(value); err == nil {
			t.Fatalf("parsePulsePath(%q) unexpectedly succeeded", value)
		}
	}

	if got, err := parsePulseMaxPacketBytes(""); err != nil || got != defaultPulseMaxPacketBytes {
		t.Fatalf("default pulse max packet = %d, %v", got, err)
	}
	if _, err := parsePulseMaxPacketBytes("100"); err == nil {
		t.Fatal("parsePulseMaxPacketBytes accepted too-small value")
	}
}
