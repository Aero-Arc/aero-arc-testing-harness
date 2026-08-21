package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	OperationQueryReferences = "dss.query_references"
	OperationGetReference    = "dss.get_reference"
	OperationCreateReference = "dss.create_reference"
	OperationUpdateReference = "dss.update_reference"
	OperationDeleteReference = "dss.delete_reference"
	OperationSubscriptions   = "dss.query_subscriptions"
	OperationPeerDetails     = "peer.get_details"
	OperationPeerNotify      = "peer.notify"
)

// FaultPlan describes a bounded protocol fault. Times must be positive.
type FaultPlan struct {
	Operation        string `json:"operation"`
	Times            int    `json:"times"`
	Status           int    `json:"status,omitempty"`
	DelayMillis      int    `json:"delay_ms,omitempty"`
	CloseAfterCommit bool   `json:"close_after_commit,omitempty"`
	MalformedJSON    bool   `json:"malformed_json,omitempty"`
}

// Event is an immutable observation from the fixture request journal.
type Event struct {
	Sequence  uint64         `json:"sequence"`
	At        time.Time      `json:"at"`
	Operation string         `json:"operation"`
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	IntentID  string         `json:"intent_id,omitempty"`
	Status    int            `json:"status"`
	Fault     *FaultSnapshot `json:"fault,omitempty"`
}

// FaultSnapshot records the exact injected behavior used for a request.
type FaultSnapshot struct {
	Status           int  `json:"status,omitempty"`
	DelayMillis      int  `json:"delay_ms,omitempty"`
	CloseAfterCommit bool `json:"close_after_commit,omitempty"`
	MalformedJSON    bool `json:"malformed_json,omitempty"`
}

// Reference is the fixture's authoritative DSS state.
type Reference struct {
	ID              string         `json:"id"`
	Manager         string         `json:"manager"`
	OVN             string         `json:"ovn"`
	State           string         `json:"state"`
	SubscriptionID  string         `json:"subscription_id"`
	USSAvailability string         `json:"uss_availability"`
	USSBaseURL      string         `json:"uss_base_url"`
	Version         int            `json:"version"`
	TimeStart       map[string]any `json:"time_start"`
	TimeEnd         map[string]any `json:"time_end"`
	Volumes         []any          `json:"-"`
}

// Config controls URLs advertised by the deterministic fixture.
type Config struct {
	Manager       string
	PeerBaseURL   string
	SubscriberURL string
	Now           func() time.Time
}

// Server implements the minimal SCD DSS and peer-USS surface needed by the
// federation E2E suite plus an isolated control plane.
type Server struct {
	mu            sync.Mutex
	manager       string
	peerBaseURL   string
	subscriberURL string
	now           func() time.Time
	sequence      atomic.Uint64
	references    map[string]Reference
	faults        map[string]FaultPlan
	events        []Event
}

// New returns an initialized deterministic fixture server.
func New(config Config) *Server {
	manager := config.Manager
	if manager == "" {
		manager = "fixture-dss"
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		manager:       manager,
		peerBaseURL:   strings.TrimRight(config.PeerBaseURL, "/"),
		subscriberURL: strings.TrimRight(config.SubscriberURL, "/"),
		now:           now,
		references:    make(map[string]Reference),
		faults:        make(map[string]FaultPlan),
	}
}

// Handler returns the fixture's HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /control/reset", s.handleReset)
	mux.HandleFunc("POST /control/faults", s.handleFault)
	mux.HandleFunc("POST /control/faults/clear", s.handleClearFaults)
	mux.HandleFunc("GET /control/events", s.handleEvents)
	mux.HandleFunc("GET /control/state", s.handleState)
	mux.HandleFunc("POST /control/intents/{intent_id}/rotate-ovn", s.handleRotateOVN)
	mux.HandleFunc("POST /dss/v1/operational_intent_references/query", s.handleQueryReferences)
	mux.HandleFunc("POST /dss/v1/subscriptions/query", s.handleSubscriptions)
	mux.HandleFunc("GET /dss/v1/operational_intent_references/{intent_id}", s.handleGetReference)
	mux.HandleFunc("PUT /dss/v1/operational_intent_references/{intent_id}", s.handleCreateReference)
	mux.HandleFunc("PUT /dss/v1/operational_intent_references/{intent_id}/{ovn}", s.handleUpdateReference)
	mux.HandleFunc("DELETE /dss/v1/operational_intent_references/{intent_id}/{ovn}", s.handleDeleteReference)
	mux.HandleFunc("GET /uss/v1/operational_intents/{intent_id}", s.handlePeerDetails)
	mux.HandleFunc("POST /uss/v1/operational_intents", s.handlePeerNotification)
	return mux
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.references = make(map[string]Reference)
	s.faults = make(map[string]FaultPlan)
	s.events = nil
	s.mu.Unlock()
	s.sequence.Store(0)
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) handleFault(w http.ResponseWriter, r *http.Request) {
	var plan FaultPlan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if plan.Operation == "" || plan.Times <= 0 || plan.DelayMillis < 0 || plan.Status < 0 || plan.Status > 599 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "operation and positive times are required; delay/status must be valid"})
		return
	}
	s.mu.Lock()
	s.faults[plan.Operation] = plan
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, plan)
}

func (s *Server) handleClearFaults(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.faults = make(map[string]FaultPlan)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	events := append([]Event(nil), s.events...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	references := make([]Reference, 0, len(s.references))
	for _, reference := range s.references {
		references = append(references, reference)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"operational_intent_references": references})
}

func (s *Server) handleRotateOVN(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("intent_id")
	s.mu.Lock()
	reference, ok := s.references[id]
	if ok {
		reference.Version++
		reference.OVN = fmt.Sprintf("ovn-%s-%d", id, reference.Version)
		s.references[id] = reference
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "reference not found"})
		return
	}
	writeJSON(w, http.StatusOK, reference)
}

func (s *Server) handleQueryReferences(w http.ResponseWriter, r *http.Request) {
	fault := s.takeFault(OperationQueryReferences)
	if s.preCommitFault(w, r, OperationQueryReferences, "", fault) {
		return
	}
	s.mu.Lock()
	references := make([]Reference, 0, len(s.references))
	for _, reference := range s.references {
		references = append(references, reference)
	}
	s.mu.Unlock()
	s.respond(w, r, OperationQueryReferences, "", http.StatusOK, map[string]any{"operational_intent_references": references}, fault)
}

func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	fault := s.takeFault(OperationSubscriptions)
	if s.preCommitFault(w, r, OperationSubscriptions, "", fault) {
		return
	}
	s.respond(w, r, OperationSubscriptions, "", http.StatusOK, map[string]any{"subscriptions": []any{}}, fault)
}

func (s *Server) handleGetReference(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("intent_id")
	fault := s.takeFault(OperationGetReference)
	if s.preCommitFault(w, r, OperationGetReference, id, fault) {
		return
	}
	s.mu.Lock()
	reference, ok := s.references[id]
	s.mu.Unlock()
	if !ok {
		s.respond(w, r, OperationGetReference, id, http.StatusNotFound, map[string]string{"message": "reference not found"}, fault)
		return
	}
	s.respond(w, r, OperationGetReference, id, http.StatusOK, map[string]any{"operational_intent_reference": reference}, fault)
}

type putRequest struct {
	State      string `json:"state"`
	USSBaseURL string `json:"uss_base_url"`
	Extents    []any  `json:"extents"`
}

func (s *Server) handleCreateReference(w http.ResponseWriter, r *http.Request) {
	s.handlePutReference(w, r, true)
}

func (s *Server) handleUpdateReference(w http.ResponseWriter, r *http.Request) {
	s.handlePutReference(w, r, false)
}

func (s *Server) handlePutReference(w http.ResponseWriter, r *http.Request, create bool) {
	id := r.PathValue("intent_id")
	operation := OperationUpdateReference
	status := http.StatusOK
	if create {
		operation = OperationCreateReference
		status = http.StatusCreated
	}
	fault := s.takeFault(operation)
	if fault != nil && fault.DelayMillis > 0 {
		if !wait(r.Context(), time.Duration(fault.DelayMillis)*time.Millisecond) {
			s.record(r, operation, id, 499, fault)
			return
		}
	}
	if fault != nil && fault.Status != 0 {
		s.respond(w, r, operation, id, fault.Status, map[string]string{"message": "injected fixture failure"}, fault)
		return
	}
	var body putRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.respond(w, r, operation, id, http.StatusBadRequest, map[string]string{"message": err.Error()}, fault)
		return
	}
	s.mu.Lock()
	current, exists := s.references[id]
	if create && exists {
		s.mu.Unlock()
		s.respond(w, r, operation, id, http.StatusConflict, map[string]string{"message": "reference exists"}, fault)
		return
	}
	if !create {
		if !exists {
			s.mu.Unlock()
			s.respond(w, r, operation, id, http.StatusNotFound, map[string]string{"message": "reference not found"}, fault)
			return
		}
		if got := r.PathValue("ovn"); got != current.OVN {
			s.mu.Unlock()
			s.respond(w, r, operation, id, http.StatusConflict, map[string]any{"message": "stale OVN", "current_ovn": current.OVN}, fault)
			return
		}
	}
	version := current.Version + 1
	start := s.now().UTC().Add(-time.Minute)
	end := start.Add(time.Hour)
	baseURL := body.USSBaseURL
	if baseURL == "" {
		baseURL = s.peerBaseURL
	}
	reference := Reference{
		ID: id, Manager: s.manager, OVN: fmt.Sprintf("ovn-%s-%d", id, version), State: body.State,
		SubscriptionID: "33333333-3333-4333-8333-333333333333", USSAvailability: "Normal",
		USSBaseURL: baseURL, Version: version, Volumes: append([]any(nil), body.Extents...),
		TimeStart: map[string]any{"format": "RFC3339", "value": start.Format(time.RFC3339)},
		TimeEnd:   map[string]any{"format": "RFC3339", "value": end.Format(time.RFC3339)},
	}
	s.references[id] = reference
	s.mu.Unlock()

	payload := map[string]any{"operational_intent_reference": reference, "subscribers": []any{}}
	if s.subscriberURL != "" {
		payload["subscribers"] = []any{map[string]any{
			"uss_base_url": s.subscriberURL,
			"subscriptions": []any{map[string]any{
				"subscription_id":    "22222222-2222-4222-8222-222222222222",
				"notification_index": 1,
			}},
		}}
	}
	if fault != nil && fault.CloseAfterCommit {
		s.record(r, operation, id, 0, fault)
		closeConnection(w)
		return
	}
	s.respond(w, r, operation, id, status, payload, fault)
}

func (s *Server) handleDeleteReference(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("intent_id")
	fault := s.takeFault(OperationDeleteReference)
	if fault != nil && fault.DelayMillis > 0 {
		if !wait(r.Context(), time.Duration(fault.DelayMillis)*time.Millisecond) {
			s.record(r, OperationDeleteReference, id, 499, fault)
			return
		}
	}
	if fault != nil && fault.Status != 0 {
		s.respond(w, r, OperationDeleteReference, id, fault.Status, map[string]string{"message": "injected fixture failure"}, fault)
		return
	}
	s.mu.Lock()
	reference, ok := s.references[id]
	if ok && r.PathValue("ovn") == reference.OVN {
		delete(s.references, id)
	}
	s.mu.Unlock()
	if !ok {
		s.respond(w, r, OperationDeleteReference, id, http.StatusNotFound, map[string]string{"message": "reference not found"}, fault)
		return
	}
	if r.PathValue("ovn") != reference.OVN {
		s.respond(w, r, OperationDeleteReference, id, http.StatusConflict, map[string]any{"message": "stale OVN", "current_ovn": reference.OVN}, fault)
		return
	}
	reference.Version++
	reference.OVN = fmt.Sprintf("ovn-%s-%d", id, reference.Version)
	payload := map[string]any{"operational_intent_reference": reference, "subscribers": []any{}}
	if fault != nil && fault.CloseAfterCommit {
		s.record(r, OperationDeleteReference, id, 0, fault)
		closeConnection(w)
		return
	}
	s.respond(w, r, OperationDeleteReference, id, http.StatusOK, payload, fault)
}

func (s *Server) handlePeerDetails(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("intent_id")
	fault := s.takeFault(OperationPeerDetails)
	if s.preCommitFault(w, r, OperationPeerDetails, id, fault) {
		return
	}
	s.mu.Lock()
	reference, ok := s.references[id]
	s.mu.Unlock()
	if !ok {
		s.respond(w, r, OperationPeerDetails, id, http.StatusNotFound, map[string]string{"message": "intent not found"}, fault)
		return
	}
	details := map[string]any{"priority": 0, "volumes": reference.Volumes, "off_nominal_volumes": []any{}}
	s.respond(w, r, OperationPeerDetails, id, http.StatusOK, map[string]any{
		"operational_intent": map[string]any{"reference": reference, "details": details},
	}, fault)
}

func (s *Server) handlePeerNotification(w http.ResponseWriter, r *http.Request) {
	fault := s.takeFault(OperationPeerNotify)
	if s.preCommitFault(w, r, OperationPeerNotify, "", fault) {
		return
	}
	s.record(r, OperationPeerNotify, "", http.StatusNoContent, fault)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) preCommitFault(w http.ResponseWriter, r *http.Request, operation, intentID string, fault *FaultSnapshot) bool {
	if fault == nil {
		return false
	}
	if fault.DelayMillis > 0 && !wait(r.Context(), time.Duration(fault.DelayMillis)*time.Millisecond) {
		s.record(r, operation, intentID, 499, fault)
		return true
	}
	if fault.Status != 0 {
		s.respond(w, r, operation, intentID, fault.Status, map[string]string{"message": "injected fixture failure"}, fault)
		return true
	}
	return false
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, operation, intentID string, status int, payload any, fault *FaultSnapshot) {
	s.record(r, operation, intentID, status, fault)
	if fault != nil && fault.MalformedJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"incomplete":`))
		return
	}
	writeJSON(w, status, payload)
}

func (s *Server) takeFault(operation string) *FaultSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.faults[operation]
	if !ok || plan.Times <= 0 {
		return nil
	}
	plan.Times--
	if plan.Times == 0 {
		delete(s.faults, operation)
	} else {
		s.faults[operation] = plan
	}
	return &FaultSnapshot{
		Status: plan.Status, DelayMillis: plan.DelayMillis,
		CloseAfterCommit: plan.CloseAfterCommit, MalformedJSON: plan.MalformedJSON,
	}
}

func (s *Server) record(r *http.Request, operation, intentID string, status int, fault *FaultSnapshot) {
	event := Event{
		Sequence: s.sequence.Add(1), At: s.now().UTC(), Operation: operation,
		Method: r.Method, Path: r.URL.RequestURI(), IntentID: intentID, Status: status, Fault: fault,
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func closeConnection(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "injected ambiguous outcome"})
		return
	}
	connection, _, err := hijacker.Hijack()
	if err == nil {
		_ = connection.Close()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// DecodeEvents decodes the response returned by GET /control/events.
func DecodeEvents(response *http.Response) ([]Event, error) {
	if response == nil {
		return nil, errors.New("nil response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("events status %d", response.StatusCode)
	}
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return body.Events, nil
}

// ParseRetryAfter converts a Retry-After header used by future fixture faults.
func ParseRetryAfter(value string) (time.Duration, error) {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("invalid Retry-After %q", value)
	}
	return time.Duration(seconds) * time.Second, nil
}
