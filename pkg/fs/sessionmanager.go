package fs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/alex-ilgayev/mcpspy/pkg/event"
)

// sessionKey uniquely identifies a filesystem session
type sessionKey struct {
	pid           uint32
	filePtr       uint64
	origEventType event.EventType
}

// session tracks filesystem communication for a single file descriptor
type session struct {
	pid     uint32
	comm    [16]uint8
	filePtr uint64

	// Buffer for accumulating data
	buf *bytes.Buffer
}

// SessionManager manages filesystem sessions and aggregates JSON payloads
type SessionManager struct {
	mu sync.Mutex

	sessions map[sessionKey]*session
	eventCh  chan event.Event
}

// NewSessionManager creates a new filesystem session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[sessionKey]*session),
		eventCh:  make(chan event.Event, 100),
	}
}

// ProcessFSEvent processes filesystem read/write events and aggregates JSON payloads
func (s *SessionManager) ProcessFSEvent(e *event.FSDataEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create session key
	key := sessionKey{
		pid:           e.PID,
		filePtr:       e.FilePtr,
		origEventType: e.EventType,
	}

	// Get or create session
	sess, exists := s.sessions[key]
	if !exists {
		sess = &session{
			pid:     e.PID,
			comm:    e.CommBytes,
			filePtr: e.FilePtr,
			buf:     &bytes.Buffer{},
		}
		s.sessions[key] = sess
	}

	// Append data to buffer
	if _, err := sess.buf.Write(e.Buffer()); err != nil {
		return err
	}

	// Try to parse JSON from the accumulated buffer
	if err := s.tryEmitJsonEvent(sess, key); err != nil {
		return err
	}

	return nil
}

// tryEmitJsonEvent attempts to parse and emit complete JSON messages
func (s *SessionManager) tryEmitJsonEvent(sess *session, key sessionKey) error {
	bufData := bytes.TrimSpace(sess.buf.Bytes())
	if len(bufData) == 0 {
		sess.buf.Reset()
		return nil
	}

	// Quick sanity check before expensive JSON parsing: must start with { or [
	if bufData[0] != '{' && bufData[0] != '[' {
		return fmt.Errorf("invalid JSON start character: %c", bufData[0])
	}

	// Use JSON decoder to parse multiple JSON objects
	reader := bytes.NewReader(bufData)
	decoder := json.NewDecoder(reader)
	lastGoodPosition := int64(0)

	for {
		var jsonData json.RawMessage
		err := decoder.Decode(&jsonData)
		if err != nil {
			if err == io.EOF {
				// Successfully processed all complete JSON objects
				break
			}
			// Syntax error or incomplete JSON - stop here and keep remaining bytes
			break
		}

		if len(bytes.TrimSpace(jsonData)) == 0 {
			continue
		}

		// Emit this JSON message
		if err := s.emitJsonEvent(sess, key, jsonData); err != nil {
			return err
		}

		// Track how many bytes we've successfully processed using InputOffset
		// which accounts for decoder's internal buffering
		lastGoodPosition = decoder.InputOffset()
	}

	// Update buffer: keep only unprocessed bytes
	if lastGoodPosition > 0 {
		remainingData := bufData[lastGoodPosition:]
		sess.buf = bytes.NewBuffer(remainingData)
	}

	return nil
}

// emitJsonEvent emits a complete JSON event
func (s *SessionManager) emitJsonEvent(sess *session, key sessionKey, payload []byte) error {
	newEventType := event.EventTypeFSJsonRead
	if key.origEventType == event.EventTypeFSWrite {
		newEventType = event.EventTypeFSJsonWrite
	}

	event := event.FSJsonEvent{
		EventHeader: event.EventHeader{
			EventType: newEventType,
			PID:       sess.pid,
			CommBytes: sess.comm,
		},
		FilePtr: sess.filePtr,
		Payload: payload,
	}

	select {
	case s.eventCh <- &event:
	default:
		return fmt.Errorf("FS event channel is full, dropping FS JSON event")
	}

	return nil
}

// FSEvents returns a channel for receiving filesystem JSON events
func (s *SessionManager) FSEvents() <-chan event.Event {
	return s.eventCh
}

// Close closes the event channel and cleans up sessions
func (s *SessionManager) Close() {
	close(s.eventCh)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all sessions
	s.sessions = make(map[sessionKey]*session)
}

// CleanupSession removes a specific session (e.g., when file is closed)
// TODO: This function should be called when a file descriptor is closed to free up resources
func (s *SessionManager) CleanupSession(pid uint32, filePtr uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete both read and write sessions for this PID+FilePtr
	keyRead := sessionKey{pid: pid, filePtr: filePtr, origEventType: event.EventTypeFSRead}
	keyWrite := sessionKey{pid: pid, filePtr: filePtr, origEventType: event.EventTypeFSWrite}
	delete(s.sessions, keyRead)
	delete(s.sessions, keyWrite)
}
