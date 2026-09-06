package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pulseSessionTTL        = 30 * time.Second
	pulseMaxSessions       = 1024
	pulseMaxBufferedPacket = 64
)

var (
	errPulseClosed       = errors.New("pulse session closed")
	errPulseModeConflict = errors.New("pulse upload mode conflict")
	errPulseQueueFull    = errors.New("pulse upload queue full")
)

type pulseUploadMode byte

const (
	pulseUploadUnknown pulseUploadMode = iota
	pulseUploadPacket
	pulseUploadStream
)

type pulseSession struct {
	upload    *pulseUploadQueue
	done      chan struct{}
	closeOnce sync.Once
	timer     *time.Timer
	connected bool
}

func newPulseSession() *pulseSession {
	return &pulseSession{
		upload: newPulseUploadQueue(),
		done:   make(chan struct{}),
	}
}

func (s *pulseSession) close() {
	s.closeOnce.Do(func() {
		if s.timer != nil {
			s.timer.Stop()
		}
		_ = s.upload.Close()
		close(s.done)
	})
}

type pulseUploadQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	mode    pulseUploadMode
	packets map[uint64][]byte
	next    uint64
	current *bytes.Reader
	stream  io.ReadCloser
	closed  bool
}

func newPulseUploadQueue() *pulseUploadQueue {
	q := &pulseUploadQueue{packets: make(map[uint64][]byte)}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *pulseUploadQueue) addPacket(seq uint64, payload []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return errPulseClosed
	}
	if q.mode == pulseUploadStream {
		return errPulseModeConflict
	}
	q.mode = pulseUploadPacket

	if seq < q.next {
		return nil
	}
	if _, exists := q.packets[seq]; exists {
		return nil
	}
	if len(q.packets) >= pulseMaxBufferedPacket {
		return errPulseQueueFull
	}

	q.packets[seq] = bytes.Clone(payload)
	q.cond.Broadcast()
	return nil
}

func (q *pulseUploadQueue) setStream(stream io.ReadCloser) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return errPulseClosed
	}
	if q.mode == pulseUploadPacket || q.stream != nil {
		return errPulseModeConflict
	}
	q.mode = pulseUploadStream
	q.stream = stream
	q.cond.Broadcast()
	return nil
}

func (q *pulseUploadQueue) Read(buffer []byte) (int, error) {
	for {
		q.mu.Lock()

		if q.current != nil {
			n, err := q.current.Read(buffer)
			if errors.Is(err, io.EOF) {
				q.current = nil
				q.next++
				q.mu.Unlock()
				if n > 0 {
					return n, nil
				}
				continue
			}
			q.mu.Unlock()
			return n, err
		}

		if q.mode == pulseUploadStream && q.stream != nil {
			stream := q.stream
			q.mu.Unlock()
			return stream.Read(buffer)
		}

		if q.mode == pulseUploadPacket {
			if payload, ok := q.packets[q.next]; ok {
				delete(q.packets, q.next)
				q.current = bytes.NewReader(payload)
				q.mu.Unlock()
				continue
			}
		}

		if q.closed {
			q.mu.Unlock()
			return 0, io.EOF
		}

		q.cond.Wait()
		q.mu.Unlock()
	}
}

func (q *pulseUploadQueue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	stream := q.stream
	q.cond.Broadcast()
	q.mu.Unlock()

	if stream != nil {
		return stream.Close()
	}
	return nil
}

type pulseHTTPWriter struct {
	writer http.ResponseWriter
}

func (w *pulseHTTPWriter) Write(payload []byte) (int, error) {
	n, err := w.writer.Write(payload)
	if flusher, ok := w.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func isPulseRequestPath(requestPath, basePath string) bool {
	if basePath == "" {
		return false
	}
	return requestPath == basePath || requestPath == basePath+"/" || strings.HasPrefix(requestPath, basePath+"/")
}

func parsePulsePathMeta(requestPath, basePath string) (sessionID string, seq uint64, hasSeq bool, err error) {
	if !isPulseRequestPath(requestPath, basePath) {
		return "", 0, false, errors.New("not a pulse path")
	}

	remainder := strings.TrimPrefix(requestPath, basePath)
	remainder = strings.TrimPrefix(remainder, "/")
	if remainder == "" {
		return "", 0, false, nil
	}

	parts := strings.Split(remainder, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", 0, false, errors.New("invalid pulse path")
	}
	if !validPulseSessionID(parts[0]) {
		return "", 0, false, errors.New("invalid pulse session")
	}

	sessionID = parts[0]
	if len(parts) == 1 {
		return sessionID, 0, false, nil
	}

	seq, err = strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return "", 0, false, errors.New("invalid pulse sequence")
	}
	return sessionID, seq, true, nil
}

func validPulseSessionID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func (s *proxyServer) getPulseSession(sessionID string) (*pulseSession, error) {
	s.pulseMu.Lock()
	defer s.pulseMu.Unlock()

	if current := s.pulseSessions[sessionID]; current != nil {
		return current, nil
	}
	if len(s.pulseSessions) >= pulseMaxSessions {
		return nil, errPulseQueueFull
	}

	session := newPulseSession()
	s.pulseSessions[sessionID] = session
	session.timer = time.AfterFunc(pulseSessionTTL, func() {
		s.expirePulseSession(sessionID, session)
	})
	return session, nil
}

func (s *proxyServer) attachPulseSession(sessionID string) (*pulseSession, error) {
	session, err := s.getPulseSession(sessionID)
	if err != nil {
		return nil, err
	}

	s.pulseMu.Lock()
	defer s.pulseMu.Unlock()
	if s.pulseSessions[sessionID] != session {
		return nil, errPulseClosed
	}
	if session.connected {
		return nil, errPulseModeConflict
	}
	session.connected = true
	if session.timer != nil {
		session.timer.Stop()
	}
	return session, nil
}

func (s *proxyServer) expirePulseSession(sessionID string, session *pulseSession) {
	s.pulseMu.Lock()
	if s.pulseSessions[sessionID] != session || session.connected {
		s.pulseMu.Unlock()
		return
	}
	delete(s.pulseSessions, sessionID)
	s.pulseMu.Unlock()
	session.close()
}

func (s *proxyServer) deletePulseSession(sessionID string, session *pulseSession) {
	s.pulseMu.Lock()
	if s.pulseSessions[sessionID] == session {
		delete(s.pulseSessions, sessionID)
	}
	s.pulseMu.Unlock()
	session.close()
}

func (s *proxyServer) closePulseSessions() {
	s.pulseMu.Lock()
	sessions := make([]*pulseSession, 0, len(s.pulseSessions))
	for _, session := range s.pulseSessions {
		sessions = append(sessions, session)
	}
	s.pulseSessions = make(map[string]*pulseSession)
	s.pulseMu.Unlock()

	for _, session := range sessions {
		session.close()
	}
}

func (s *proxyServer) handlePulse(w http.ResponseWriter, r *http.Request) {
	writePulseCommonHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	sessionID, seq, hasSeq, err := parsePulsePathMeta(r.URL.Path, s.pulsePath)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if sessionID == "" {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Error(w, "stream request requires a body", http.StatusBadRequest)
			return
		}
		s.handlePulseStreamOne(w, r)
		return
	}

	if hasSeq {
		s.handlePulsePacket(w, r, sessionID, seq)
		return
	}

	if r.Method == http.MethodGet {
		s.handlePulseDownlink(w, r, sessionID)
		return
	}
	if r.Method == http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handlePulseStreamUpload(w, r, sessionID)
}

func writePulseCommonHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if r.Method == http.MethodOptions {
		if method := r.Header.Get("Access-Control-Request-Method"); method != "" {
			w.Header().Set("Access-Control-Allow-Methods", method)
		} else {
			w.Header().Set("Access-Control-Allow-Methods", "*")
		}
		if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
			w.Header().Set("Access-Control-Allow-Headers", headers)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "*")
		}
	}
}

func preparePulseDownload(w http.ResponseWriter) {
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func enablePulseFullDuplex(w http.ResponseWriter) {
	_ = http.NewResponseController(w).EnableFullDuplex()
}

func (s *proxyServer) handlePulseStreamOne(w http.ResponseWriter, r *http.Request) {
	enablePulseFullDuplex(w)
	preparePulseDownload(w)
	defer r.Body.Close()

	writer := &pulseHTTPWriter{writer: w}
	s.relayPulse(r.Body, writer)
}

func (s *proxyServer) handlePulseDownlink(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, err := s.attachPulseSession(sessionID)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusConflict)
		return
	}
	defer s.deletePulseSession(sessionID, session)

	stop := context.AfterFunc(r.Context(), func() { _ = session.upload.Close() })
	defer stop()

	preparePulseDownload(w)
	writer := &pulseHTTPWriter{writer: w}
	s.relayPulse(session.upload, writer)
}

func (s *proxyServer) handlePulseStreamUpload(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, err := s.getPulseSession(sessionID)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := session.upload.setStream(r.Body); err != nil {
		http.Error(w, "session conflict", http.StatusConflict)
		return
	}

	enablePulseFullDuplex(w)
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	select {
	case <-session.done:
	case <-r.Context().Done():
		_ = session.upload.Close()
	}
}

func (s *proxyServer) handlePulsePacket(w http.ResponseWriter, r *http.Request, sessionID string, seq uint64) {
	if r.ContentLength > s.pulseMaxPacketBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	payload, err := io.ReadAll(io.LimitReader(r.Body, s.pulseMaxPacketBytes+1))
	if err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if int64(len(payload)) > s.pulseMaxPacketBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	session, err := s.getPulseSession(sessionID)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := session.upload.addPacket(seq, payload); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errPulseQueueFull) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "session unavailable", status)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (s *proxyServer) relayPulse(reader io.Reader, writer io.Writer) {
	request, err := readVLESSRequest(reader, s.uuid)
	if err != nil {
		return
	}

	switch request.command {
	case commandTCP:
		s.relayPulseTCP(reader, writer, request)
	case commandUDP:
		s.relayPulseUDP(reader, writer, request)
	}
}

func (s *proxyServer) relayPulseTCP(reader io.Reader, writer io.Writer, request vlessRequest) {
	target, err := s.dialer.Dial("tcp", destination(request.address, request.port))
	if err != nil {
		return
	}
	defer target.Close()

	if _, err := writer.Write([]byte{0, 0}); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.CopyBuffer(target, reader, make([]byte, tcpRelayBufferSize))
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.CopyBuffer(writer, target, make([]byte, tcpRelayBufferSize))
		done <- struct{}{}
	}()

	<-done
	_ = target.Close()
	closePulseReader(reader)
	<-done
}

func (s *proxyServer) relayPulseUDP(reader io.Reader, writer io.Writer, request vlessRequest) {
	target, err := s.dialer.Dial("udp", destination(request.address, request.port))
	if err != nil {
		return
	}
	defer target.Close()

	if _, err := writer.Write([]byte{0, 0}); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		relayUDPToTarget(reader, target)
		done <- struct{}{}
	}()
	go func() {
		relayUDPToWebSocket(target, writer)
		done <- struct{}{}
	}()

	<-done
	_ = target.Close()
	closePulseReader(reader)
	<-done
}

func closePulseReader(reader io.Reader) {
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
}

var _ io.ReadCloser = (*pulseUploadQueue)(nil)
