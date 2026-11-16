package admission

import "slices"

// Store holds events, open requests, and admissions. The Service
// serializes every call, so implementations need no locking of their own.
// Persistent storage remains a separate decision (docs/backlog.md).
type Store interface {
	Event(id string) (Event, bool)
	PutEvent(event Event)
	// Requests returns the open requests for one event.
	Requests(eventID string) []Request
	PutRequest(request Request)
	DeleteRequest(eventID, deviceID string)
	Admission(eventID, deviceID string) (Admission, bool)
	PutAdmission(admission Admission)
}

type deviceKey struct {
	eventID  string
	deviceID string
}

// MemoryStore keeps all state in memory. It is not safe for concurrent
// use outside a Service.
type MemoryStore struct {
	events     map[string]Event
	requests   map[deviceKey]Request
	admissions map[deviceKey]Admission
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:     make(map[string]Event),
		requests:   make(map[deviceKey]Request),
		admissions: make(map[deviceKey]Admission),
	}
}

// Event returns one event by identifier.
func (s *MemoryStore) Event(id string) (Event, bool) {
	event, ok := s.events[id]
	return event, ok
}

// PutEvent stores an event, replacing one with the same identifier.
func (s *MemoryStore) PutEvent(event Event) {
	s.events[event.ID] = event
}

// Requests returns the open requests for one event.
func (s *MemoryStore) Requests(eventID string) []Request {
	requests := make([]Request, 0)
	for key, request := range s.requests {
		if key.eventID == eventID {
			requests = append(requests, cloneRequest(request))
		}
	}
	return requests
}

// PutRequest stores a request, replacing the device's earlier request.
func (s *MemoryStore) PutRequest(request Request) {
	s.requests[deviceKey{request.EventID, request.DeviceID}] = cloneRequest(request)
}

// DeleteRequest removes one device's request for an event.
func (s *MemoryStore) DeleteRequest(eventID, deviceID string) {
	delete(s.requests, deviceKey{eventID, deviceID})
}

// Admission returns one device's admission for an event.
func (s *MemoryStore) Admission(eventID, deviceID string) (Admission, bool) {
	admission, ok := s.admissions[deviceKey{eventID, deviceID}]
	if ok {
		admission.PublicKey = slices.Clone(admission.PublicKey)
	}
	return admission, ok
}

// PutAdmission stores an admission, replacing the device's earlier one.
func (s *MemoryStore) PutAdmission(admission Admission) {
	admission.PublicKey = slices.Clone(admission.PublicKey)
	s.admissions[deviceKey{admission.EventID, admission.DeviceID}] = admission
}

func cloneRequest(request Request) Request {
	request.PublicKey = slices.Clone(request.PublicKey)
	return request
}
