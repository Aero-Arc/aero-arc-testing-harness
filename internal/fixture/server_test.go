package fixture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReferenceLifecycleAndStaleOVN(t *testing.T) {
	handler := New(Config{PeerBaseURL: "http://peer.test"}).Handler()

	id := "11111111-1111-4111-8111-111111111111"
	created := doJSON(t, handler, http.MethodPut, "/dss/v1/operational_intent_references/"+id, map[string]any{"state": "Accepted"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d", created.Code)
	}

	rotated := doJSON(t, handler, http.MethodPost, "/control/intents/"+id+"/rotate-ovn", nil)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status = %d", rotated.Code)
	}

	deleted := doJSON(t, handler, http.MethodDelete, "/dss/v1/operational_intent_references/"+id+"/ovn-"+id+"-1", nil)
	if deleted.Code != http.StatusConflict {
		t.Fatalf("stale delete status = %d", deleted.Code)
	}

	current := doJSON(t, handler, http.MethodGet, "/dss/v1/operational_intent_references/"+id, nil)
	if current.Code != http.StatusOK {
		t.Fatalf("get status = %d", current.Code)
	}
}

func TestBoundedFaultIsConsumed(t *testing.T) {
	handler := New(Config{}).Handler()

	armed := doJSON(t, handler, http.MethodPost, "/control/faults", FaultPlan{
		Operation: OperationQueryReferences, Times: 1, Status: http.StatusServiceUnavailable,
	})
	if armed.Code != http.StatusCreated {
		t.Fatalf("arm status = %d", armed.Code)
	}

	first := doJSON(t, handler, http.MethodPost, "/dss/v1/operational_intent_references/query", map[string]any{})
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d", first.Code)
	}
	second := doJSON(t, handler, http.MethodPost, "/dss/v1/operational_intent_references/query", map[string]any{})
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, path, &payload)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
