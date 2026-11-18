package admission

import (
	"slices"
	"strings"
)

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
	// Admissions returns the admissions for one event, ordered by device
	// identifier.
	Admissions(eventID string) []Admission
	PutAdmission(admission Admission)
}

type deviceKey struct {
	eventID  string
	deviceID string
}

// MemoryStore keeps all state in memory. It is not safe for concurrent
// use outside a Service.
type MemoryStore struct {
	events map[string]Event
	// requests indexes open requests by event, then by device. A lookup
	// touches only one event's requests.
	requests   map[string]map[string]Request
	admissions map[deviceKey]Admission
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:     make(map[string]Event),
		requests:   make(map[string]map[string]Request),
		admissions: make(map[deviceKey]Admission),
	}
}

// Event returns one event by identifier.
func (s *MemoryStore) Event(id string) (Event, bool) {
	event, ok := s.events[id]
	return cloneEvent(event), ok
}

// PutEvent stores an event, replacing one with the same identifier.
func (s *MemoryStore) PutEvent(event Event) {
	s.events[event.ID] = cloneEvent(event)
}

// Requests returns the open requests for one event.
func (s *MemoryStore) Requests(eventID string) []Request {
	byDevice := s.requests[eventID]
	requests := make([]Request, 0, len(byDevice))
	for _, request := range byDevice {
		requests = append(requests, cloneRequest(request))
	}
	return requests
}

// PutRequest stores a request, replacing the device's earlier request.
func (s *MemoryStore) PutRequest(request Request) {
	byDevice, ok := s.requests[request.EventID]
	if !ok {
		byDevice = make(map[string]Request)
		s.requests[request.EventID] = byDevice
	}
	byDevice[request.DeviceID] = cloneRequest(request)
}

// DeleteRequest removes one device's request for an event.
func (s *MemoryStore) DeleteRequest(eventID, deviceID string) {
	byDevice := s.requests[eventID]
	delete(byDevice, deviceID)
	if len(byDevice) == 0 {
		delete(s.requests, eventID)
	}
}

// Admission returns one device's admission for an event.
func (s *MemoryStore) Admission(eventID, deviceID string) (Admission, bool) {
	admission, ok := s.admissions[deviceKey{eventID, deviceID}]
	if ok {
		admission.PublicKey = slices.Clone(admission.PublicKey)
	}
	return admission, ok
}

// Admissions returns the admissions for one event, ordered by device
// identifier.
func (s *MemoryStore) Admissions(eventID string) []Admission {
	admissions := make([]Admission, 0)
	for key, admission := range s.admissions {
		if key.eventID != eventID {
			continue
		}
		admission.PublicKey = slices.Clone(admission.PublicKey)
		admissions = append(admissions, admission)
	}
	slices.SortFunc(admissions, func(a, b Admission) int {
		return strings.Compare(a.DeviceID, b.DeviceID)
	})
	return admissions
}

// PutAdmission stores an admission, replacing the device's earlier one.
func (s *MemoryStore) PutAdmission(admission Admission) {
	admission.PublicKey = slices.Clone(admission.PublicKey)
	s.admissions[deviceKey{admission.EventID, admission.DeviceID}] = admission
}

// cloneEvent copies the grace value so stored events share no mutable
// state with callers. Grace bounds admission validity.
func cloneEvent(event Event) Event {
	if event.Grace != nil {
		grace := *event.Grace
		event.Grace = &grace
	}
	return event
}

func cloneRequest(request Request) Request {
	request.PublicKey = slices.Clone(request.PublicKey)
	return request
}
