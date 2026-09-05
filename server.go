package main

import (
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var websocketEarlyDataReplacer = strings.NewReplacer("+", "-", "/", "_", "=", "")

type proxyServer struct {
	uuid              [16]byte
	wsPath            string
	maxMessageBytes   int64
	dialer            net.Dialer
	connectionMu      sync.Mutex
	activeConnections map[net.Conn]struct{}
}

func newProxyServer(cfg config) *proxyServer {
	return &proxyServer{
		uuid:            cfg.uuid,
		wsPath:          cfg.wsPath,
		maxMessageBytes: cfg.maxMessageBytes,
		dialer: net.Dialer{
			Timeout:       dialTimeout,
			KeepAlive:     30 * time.Second,
			FallbackDelay: dialFallbackDelay,
		},
	}
}

func (s *proxyServer) trackConnection(conn net.Conn) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()

	if s.activeConnections == nil {
		s.activeConnections = make(map[net.Conn]struct{})
	}
	s.activeConnections[conn] = struct{}{}
}

func (s *proxyServer) untrackConnection(conn net.Conn) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	delete(s.activeConnections, conn)
}

func (s *proxyServer) closeConnections() {
	s.connectionMu.Lock()
	connections := make([]net.Conn, 0, len(s.activeConnections))
	for conn := range s.activeConnections {
		connections = append(connections, conn)
	}
	s.connectionMu.Unlock()

	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (s *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == s.wsPath && isWebSocketUpgradeAttempt(r) {
		s.handleVLESSUpgrade(w, r)
		return
	}

	switch r.URL.Path {
	case "/":
		serveIndex(w, r)
	case "/healthz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, "ok\n")
		}
	case "/assets/js/main.js":
		serveMainJavaScript(w, r)
	case s.wsPath:
		s.handleVLESSUpgrade(w, r)
	default:
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
	}
}

func (s *proxyServer) handleVLESSUpgrade(w http.ResponseWriter, r *http.Request) {
	key, err := validateWebSocketRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	earlyData, hasEarlyData, err := decodeWebSocketEarlyData(r.Header.Get("Sec-WebSocket-Protocol"), s.maxMessageBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestHeaderFieldsTooLarge)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijacking is unavailable", http.StatusInternalServerError)
		return
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	s.trackConnection(conn)
	defer s.untrackConnection(conn)
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + websocketAcceptKey(key) + "\r\n"
	if hasEarlyData {
		response += "Sec-WebSocket-Protocol: " + r.Header.Get("Sec-WebSocket-Protocol") + "\r\n"
	}
	response += "\r\n"

	if _, err := rw.WriteString(response); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	ws := &wsConn{
		conn:            conn,
		bufrd:           rw.Reader,
		bufwr:           rw.Writer,
		maxMessageBytes: s.maxMessageBytes,
	}

	firstMessage := earlyData
	if !hasEarlyData {
		firstMessage, err = ws.NextMessage()
		if err != nil {
			return
		}
	}

	request, err := parseVLESSRequest(firstMessage, s.uuid)
	if err != nil {
		return
	}

	_ = conn.SetDeadline(time.Time{})

	switch request.command {
	case commandTCP:
		s.handleTCP(ws, request)
	case commandUDP:
		s.handleUDP(ws, request)
	}
}

func decodeWebSocketEarlyData(protocol string, maxMessageBytes int64) ([]byte, bool, error) {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return nil, false, nil
	}

	encoded := websocketEarlyDataReplacer.Replace(protocol)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, false, nil
	}
	if int64(len(decoded)) > maxMessageBytes {
		return nil, false, errMessageTooBig
	}
	return decoded, true, nil
}

func isWebSocketUpgradeAttempt(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

func validateWebSocketRequest(r *http.Request) (string, error) {
	if r.Method != http.MethodGet {
		return "", errors.New("websocket upgrade requires GET")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		return "", errors.New("Connection header must include Upgrade")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return "", errors.New("Upgrade header must be websocket")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return "", errors.New("Sec-WebSocket-Version must be 13")
	}

	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		return "", errors.New("Sec-WebSocket-Key is invalid")
	}
	return key, nil
}

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func destination(address string, port uint16) string {
	return net.JoinHostPort(address, strconv.Itoa(int(port)))
}
