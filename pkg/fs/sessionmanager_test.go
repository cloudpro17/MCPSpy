package fs

import (
	"testing"
	"time"

	"github.com/alex-ilgayev/mcpspy/pkg/event"
)

func TestSessionManager_SingleCompleteJson(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	jsonData := []byte(`{"jsonrpc":"2.0","method":"test","id":1}`)

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       1234,
		},
		FilePtr: 0x7fff12345678,
		Size:    uint32(len(jsonData)),
		BufSize: uint32(len(jsonData)),
	}
	copy(fsEvent.Buf[:], jsonData)

	err := sm.ProcessFSEvent(fsEvent)
	if err != nil {
		t.Fatalf("ProcessFSEvent failed: %v", err)
	}

	// Should receive one FSJsonEvent
	select {
	case evt := <-sm.FSEvents():
		if evt.Type() != event.EventTypeFSJsonRead {
			t.Errorf("Expected EventTypeFSJsonRead, got %v", evt.Type())
		}
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != string(jsonData) {
			t.Errorf("Expected payload %q, got %q", jsonData, fsJsonEvt.Payload)
		}
		if fsJsonEvt.PID != 1234 {
			t.Errorf("Expected PID 1234, got %d", fsJsonEvt.PID)
		}
		if fsJsonEvt.FilePtr != 0x7fff12345678 {
			t.Errorf("Expected FilePtr 0x7fff12345678, got 0x%x", fsJsonEvt.FilePtr)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("No FSJsonEvent received")
	}
}

func TestSessionManager_FragmentedJson(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	pid := uint32(5678)
	filePtr := uint64(0xabcdef123456)

	// Send JSON in three fragments
	fragment1 := []byte(`{"jsonrpc":"2.0","me`)
	fragment2 := []byte(`thod":"test",`)
	fragment3 := []byte(`"id":1}`)

	// First fragment
	event1 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(fragment1)),
	}
	copy(event1.Buf[:], fragment1)
	sm.ProcessFSEvent(event1)

	// Should not emit yet (incomplete JSON)
	select {
	case <-sm.FSEvents():
		t.Fatal("Should not emit event for incomplete JSON")
	case <-time.After(50 * time.Millisecond):
		// Expected - no event
	}

	// Second fragment
	event2 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(fragment2)),
	}
	copy(event2.Buf[:], fragment2)
	sm.ProcessFSEvent(event2)

	// Still incomplete
	select {
	case <-sm.FSEvents():
		t.Fatal("Should not emit event for incomplete JSON")
	case <-time.After(50 * time.Millisecond):
		// Expected - no event
	}

	// Third fragment - completes the JSON
	event3 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(fragment3)),
	}
	copy(event3.Buf[:], fragment3)
	sm.ProcessFSEvent(event3)

	// Now should emit complete JSON
	expectedJson := `{"jsonrpc":"2.0","method":"test","id":1}`
	select {
	case evt := <-sm.FSEvents():
		if evt.Type() != event.EventTypeFSJsonWrite {
			t.Errorf("Expected EventTypeFSJsonWrite, got %v", evt.Type())
		}
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != expectedJson {
			t.Errorf("Expected payload %q, got %q", expectedJson, fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("No FSJsonEvent received after complete JSON")
	}
}

func TestSessionManager_MultipleJsonInOneEvent(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	// Two complete JSON objects in one event
	jsonData := []byte(`{"id":1,"method":"first"}
{"id":2,"method":"second"}`)

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       9999,
		},
		FilePtr: 0x1111111111,
		BufSize: uint32(len(jsonData)),
	}
	copy(fsEvent.Buf[:], jsonData)

	err := sm.ProcessFSEvent(fsEvent)
	if err != nil {
		t.Fatalf("ProcessFSEvent failed: %v", err)
	}

	// Should receive two separate FSJsonEvents
	expectedPayloads := []string{
		`{"id":1,"method":"first"}`,
		`{"id":2,"method":"second"}`,
	}

	for i, expectedPayload := range expectedPayloads {
		select {
		case evt := <-sm.FSEvents():
			if evt.Type() != event.EventTypeFSJsonRead {
				t.Errorf("Event %d: Expected EventTypeFSJsonRead, got %v", i, evt.Type())
			}
			fsJsonEvt := evt.(*event.FSJsonEvent)
			if string(fsJsonEvt.Payload) != expectedPayload {
				t.Errorf("Event %d: Expected payload %q, got %q", i, expectedPayload, fsJsonEvt.Payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Did not receive FSJsonEvent %d", i)
		}
	}
}

func TestSessionManager_MultipleJsonAcrossFragments(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	pid := uint32(4444)
	filePtr := uint64(0x2222222222)

	// First event: complete JSON + start of second
	event1Data := []byte(`{"id":1}
{"id":2,"dat`)
	event1 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(event1Data)),
	}
	copy(event1.Buf[:], event1Data)
	sm.ProcessFSEvent(event1)

	// Should emit first complete JSON
	select {
	case evt := <-sm.FSEvents():
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != `{"id":1}` {
			t.Errorf("Expected first JSON, got %q", fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive first JSON")
	}

	// Second event: complete the second JSON + third complete JSON
	event2Data := []byte(`a":"value"}
{"id":3}`)
	event2 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(event2Data)),
	}
	copy(event2.Buf[:], event2Data)
	sm.ProcessFSEvent(event2)

	// Should emit second JSON
	select {
	case evt := <-sm.FSEvents():
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != `{"id":2,"data":"value"}` {
			t.Errorf("Expected second JSON, got %q", fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive second JSON")
	}

	// Should emit third JSON
	select {
	case evt := <-sm.FSEvents():
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != `{"id":3}` {
			t.Errorf("Expected third JSON, got %q", fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive third JSON")
	}
}

func TestSessionManager_MultipleSessions(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	// Session 1
	pid1 := uint32(1111)
	filePtr1 := uint64(0xaaaa)
	json1 := []byte(`{"session":1}`)

	event1 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid1,
		},
		FilePtr: filePtr1,
		BufSize: uint32(len(json1)),
	}
	copy(event1.Buf[:], json1)
	sm.ProcessFSEvent(event1)

	// Session 2 (different PID)
	pid2 := uint32(2222)
	filePtr2 := uint64(0xbbbb)
	json2 := []byte(`{"session":2}`)

	event2 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       pid2,
		},
		FilePtr: filePtr2,
		BufSize: uint32(len(json2)),
	}
	copy(event2.Buf[:], json2)
	sm.ProcessFSEvent(event2)

	// Session 3 (same PID as session 1, but different file pointer)
	filePtr3 := uint64(0xcccc)
	json3 := []byte(`{"session":3}`)

	event3 := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid1,
		},
		FilePtr: filePtr3,
		BufSize: uint32(len(json3)),
	}
	copy(event3.Buf[:], json3)
	sm.ProcessFSEvent(event3)

	// Should receive all three events
	receivedEvents := make(map[string]*event.FSJsonEvent)

	for i := 0; i < 3; i++ {
		select {
		case evt := <-sm.FSEvents():
			fsJsonEvt := evt.(*event.FSJsonEvent)
			receivedEvents[string(fsJsonEvt.Payload)] = fsJsonEvt
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Expected 3 events, only received %d", i)
		}
	}

	// Verify all sessions
	if evt, ok := receivedEvents[`{"session":1}`]; !ok {
		t.Error("Did not receive event for session 1")
	} else {
		if evt.PID != pid1 {
			t.Errorf("Session 1: expected PID %d, got %d", pid1, evt.PID)
		}
		if evt.FilePtr != filePtr1 {
			t.Errorf("Session 1: expected FilePtr 0x%x, got 0x%x", filePtr1, evt.FilePtr)
		}
		if evt.Type() != event.EventTypeFSJsonRead {
			t.Errorf("Session 1: expected read event type")
		}
	}

	if evt, ok := receivedEvents[`{"session":2}`]; !ok {
		t.Error("Did not receive event for session 2")
	} else {
		if evt.PID != pid2 {
			t.Errorf("Session 2: expected PID %d, got %d", pid2, evt.PID)
		}
		if evt.FilePtr != filePtr2 {
			t.Errorf("Session 2: expected FilePtr 0x%x, got 0x%x", filePtr2, evt.FilePtr)
		}
		if evt.Type() != event.EventTypeFSJsonWrite {
			t.Errorf("Session 2: expected write event type")
		}
	}

	if evt, ok := receivedEvents[`{"session":3}`]; !ok {
		t.Error("Did not receive event for session 3")
	} else {
		if evt.PID != pid1 {
			t.Errorf("Session 3: expected PID %d, got %d", pid1, evt.PID)
		}
		if evt.FilePtr != filePtr3 {
			t.Errorf("Session 3: expected FilePtr 0x%x, got 0x%x", filePtr3, evt.FilePtr)
		}
	}
}

func TestSessionManager_WhitespaceHandling(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	// JSON with leading/trailing whitespace
	jsonData := []byte(`
	{"id":1}
  {"id":2}
`)

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       7777,
		},
		FilePtr: 0x3333,
		BufSize: uint32(len(jsonData)),
	}
	copy(fsEvent.Buf[:], jsonData)

	sm.ProcessFSEvent(fsEvent)

	// Should receive both JSON objects (whitespace trimmed)
	for i := 1; i <= 2; i++ {
		select {
		case evt := <-sm.FSEvents():
			fsJsonEvt := evt.(*event.FSJsonEvent)
			expectedPayload := `{"id":` + string(rune('0'+i)) + `}`
			if string(fsJsonEvt.Payload) != expectedPayload {
				t.Errorf("Event %d: Expected %q, got %q", i, expectedPayload, fsJsonEvt.Payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Did not receive event %d", i)
		}
	}
}

func TestSessionManager_NestedStructures(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	complexJson := []byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"test","arguments":{"nested":{"deeply":{"value":[1,2,3]}}}}}`)

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       8888,
		},
		FilePtr: 0x4444,
		BufSize: uint32(len(complexJson)),
	}
	copy(fsEvent.Buf[:], complexJson)

	sm.ProcessFSEvent(fsEvent)

	select {
	case evt := <-sm.FSEvents():
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != string(complexJson) {
			t.Errorf("Expected complex JSON, got %q", fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive complex JSON event")
	}
}

func TestSessionManager_CleanupSession(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	pid := uint32(6666)
	filePtr := uint64(0x5555)

	// Send incomplete JSON
	incompleteJson := []byte(`{"incomplete":`)
	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(incompleteJson)),
	}
	copy(fsEvent.Buf[:], incompleteJson)
	sm.ProcessFSEvent(fsEvent)

	// Verify session exists
	sm.mu.Lock()
	key := sessionKey{pid: pid, filePtr: filePtr, origEventType: event.EventTypeFSRead}
	_, exists := sm.sessions[key]
	sm.mu.Unlock()
	if !exists {
		t.Fatal("Session should exist after processing incomplete JSON")
	}

	// Cleanup session
	sm.CleanupSession(pid, filePtr)

	// Verify sessions are deleted
	sm.mu.Lock()
	_, exists1 := sm.sessions[sessionKey{pid: pid, filePtr: filePtr, origEventType: event.EventTypeFSRead}]
	_, exists2 := sm.sessions[sessionKey{pid: pid, filePtr: filePtr, origEventType: event.EventTypeFSWrite}]
	sm.mu.Unlock()
	if exists1 || exists2 {
		t.Fatal("Sessions should be deleted after CleanupSession")
	}
}

func TestSessionManager_EmptyBuffer(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       3333,
		},
		FilePtr: 0x6666,
		BufSize: 0,
	}

	err := sm.ProcessFSEvent(fsEvent)
	if err != nil {
		t.Fatalf("ProcessFSEvent should handle empty buffer: %v", err)
	}

	// Should not emit any event
	select {
	case <-sm.FSEvents():
		t.Fatal("Should not emit event for empty buffer")
	case <-time.After(50 * time.Millisecond):
		// Expected - no event
	}
}

func TestSessionManager_JsonArray(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	jsonArray := []byte(`[{"id":1},{"id":2},{"id":3}]`)

	fsEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       5555,
		},
		FilePtr: 0x7777,
		BufSize: uint32(len(jsonArray)),
	}
	copy(fsEvent.Buf[:], jsonArray)

	sm.ProcessFSEvent(fsEvent)

	// Should receive the complete array as one event
	select {
	case evt := <-sm.FSEvents():
		fsJsonEvt := evt.(*event.FSJsonEvent)
		if string(fsJsonEvt.Payload) != string(jsonArray) {
			t.Errorf("Expected array %q, got %q", jsonArray, fsJsonEvt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive JSON array event")
	}
}

func TestSessionManager_ReadWriteEventTypes(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Close()

	pid := uint32(1000)
	filePtr := uint64(0x8888)

	// Read event
	readJson := []byte(`{"type":"read"}`)
	readEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSRead,
			PID:       pid,
		},
		FilePtr: filePtr,
		BufSize: uint32(len(readJson)),
	}
	copy(readEvent.Buf[:], readJson)
	sm.ProcessFSEvent(readEvent)

	// Write event (different file pointer to create separate session)
	writeJson := []byte(`{"type":"write"}`)
	writeEvent := &event.FSDataEvent{
		EventHeader: event.EventHeader{
			EventType: event.EventTypeFSWrite,
			PID:       pid,
		},
		FilePtr: filePtr + 1,
		BufSize: uint32(len(writeJson)),
	}
	copy(writeEvent.Buf[:], writeJson)
	sm.ProcessFSEvent(writeEvent)

	// Check read event type
	select {
	case evt := <-sm.FSEvents():
		if evt.Type() != event.EventTypeFSJsonRead {
			t.Errorf("Expected EventTypeFSJsonRead, got %v", evt.Type())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive read event")
	}

	// Check write event type
	select {
	case evt := <-sm.FSEvents():
		if evt.Type() != event.EventTypeFSJsonWrite {
			t.Errorf("Expected EventTypeFSJsonWrite, got %v", evt.Type())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive write event")
	}
}
