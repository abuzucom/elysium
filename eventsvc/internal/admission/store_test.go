package admission

import (
	"fmt"
	"testing"
)

func TestMemoryStoreRequestsPartitionByEvent(t *testing.T) {
	store := NewMemoryStore()
	const perEvent = 50
	for _, eventID := range []string{"event-a", "event-b"} {
		for i := range perEvent {
			store.PutRequest(Request{EventID: eventID, DeviceID: fmt.Sprintf("device-%d", i)})
		}
	}
	for _, eventID := range []string{"event-a", "event-b"} {
		requests := store.Requests(eventID)
		if len(requests) != perEvent {
			t.Fatalf("event %s returned %d requests, want %d", eventID, len(requests), perEvent)
		}
		for _, request := range requests {
			if request.EventID != eventID {
				t.Fatalf("event %s returned a request for %s", eventID, request.EventID)
			}
		}
	}
}

func TestMemoryStoreDropsEmptyEventIndex(t *testing.T) {
	store := NewMemoryStore()
	store.PutRequest(Request{EventID: testEventID, DeviceID: "device-1"})
	store.DeleteRequest(testEventID, "device-1")
	// The per-event index holds no empty entries, so lookups stay
	// proportional to one event's open requests.
	if _, ok := store.requests[testEventID]; ok {
		t.Fatal("empty event index retained after its last request was deleted")
	}
	if requests := store.Requests(testEventID); len(requests) != 0 {
		t.Fatalf("deleted request still returned: %v", requests)
	}
}
