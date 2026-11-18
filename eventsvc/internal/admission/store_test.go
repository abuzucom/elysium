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

func TestMemoryStoreAdmissionsAfterPagesInDeviceOrder(t *testing.T) {
	store := NewMemoryStore()
	devices := []string{"device-c", "device-a", "device-e", "device-b", "device-d"}
	for _, deviceID := range devices {
		store.PutAdmission(Admission{EventID: "event-a", DeviceID: deviceID})
	}
	store.PutAdmission(Admission{EventID: "event-b", DeviceID: "device-0"})
	// Replacing an admission must not duplicate it in the order.
	store.PutAdmission(Admission{EventID: "event-a", DeviceID: "device-c", Revoked: true})
	want := [][]string{{"device-a", "device-b"}, {"device-c", "device-d"}, {"device-e"}, {}}
	after := ""
	for i, page := range want {
		got := store.AdmissionsAfter("event-a", after, 2)
		if len(got) != len(page) {
			t.Fatalf("page %d = %v, want %v", i, got, page)
		}
		for j, admission := range got {
			if admission.DeviceID != page[j] || admission.EventID != "event-a" {
				t.Fatalf("page %d entry %d = %s/%s, want event-a/%s", i, j, admission.EventID, admission.DeviceID, page[j])
			}
			after = admission.DeviceID
		}
	}
	if got := store.AdmissionsAfter("event-a", "device-b", 1); len(got) != 1 || !got[0].Revoked {
		t.Fatalf("replaced admission = %v, want the revoked replacement", got)
	}
	if got := store.AdmissionsAfter("missing", "", 10); len(got) != 0 {
		t.Fatalf("missing event returned %v", got)
	}
}
