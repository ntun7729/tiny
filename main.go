package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultPort               = 8080
	defaultWSPath             = "/assets/js/main.js"
	defaultMaxMessageBytes    = 4 << 20
	maxConfiguredMessageBytes = 64 << 20
	handshakeTimeout          = 10 * time.Second
	dialTimeout               = 10 * time.Second
	dialFallbackDelay         = 100 * time.Millisecond
)

type config struct {
	uuid            [16]byte
	port            int
	wsPath          string
	maxMessageBytes int64
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	handler := newProxyServer(cfg)
	httpServer := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", httpServer.Addr, err)
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	log.Printf("listening addr=%s ws_path=%s", listener.Addr(), cfg.wsPath)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	case <-shutdownCtx.Done():
		handler.closeConnections()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve during shutdown: %v", err)
		}
	}
}

func loadConfig() (config, error) {
	uuid, err := parseUUID(os.Getenv("UUID"))
	if err != nil {
		return config{}, fmt.Errorf("UUID: %w", err)
	}

	port, err := parsePort(os.Getenv("PORT"))
	if err != nil {
		return config{}, fmt.Errorf("PORT: %w", err)
	}

	wsPath, err := parseWSPath(os.Getenv("WS_PATH"))
	if err != nil {
		return config{}, fmt.Errorf("WS_PATH: %w", err)
	}

	maxMessageBytes, err := parseMaxMessageBytes(os.Getenv("MAX_WS_MESSAGE_BYTES"))
	if err != nil {
		return config{}, fmt.Errorf("MAX_WS_MESSAGE_BYTES: %w", err)
	}

	return config{
		uuid:            uuid,
		port:            port,
		wsPath:          wsPath,
		maxMessageBytes: maxMessageBytes,
	}, nil
}

func parseUUID(value string) ([16]byte, error) {
	var uuid [16]byte
	value = strings.TrimSpace(value)
	if value == "" {
		return uuid, errors.New("is required")
	}

	compact := strings.ReplaceAll(value, "-", "")
	if len(compact) != 32 {
		return uuid, errors.New("must contain 32 hexadecimal characters")
	}

	decoded, err := hex.DecodeString(compact)
	if err != nil {
		return uuid, errors.New("must be a valid UUID")
	}
	copy(uuid[:], decoded)

	var zero [16]byte
	if subtle.ConstantTimeCompare(uuid[:], zero[:]) == 1 {
		return uuid, errors.New("must not be the all-zero UUID")
	}
	return uuid, nil
}

func parsePort(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultPort, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("must be an integer from 1 to 65535")
	}
	return port, nil
}

func parseWSPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultWSPath, nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if strings.ContainsAny(value, "?#") {
		return "", errors.New("must be a path without a query string or fragment")
	}
	if path.Clean(value) != value {
		return "", errors.New("must be a clean absolute path")
	}
	if value == "/healthz" {
		return "", errors.New("conflicts with the health endpoint")
	}
	return value, nil
}

func parseMaxMessageBytes(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return defaultMaxMessageBytes, nil
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil || size < 1024 || size > maxConfiguredMessageBytes {
		return 0, fmt.Errorf("must be between 1024 and %d", maxConfiguredMessageBytes)
	}
	return size, nil
}
