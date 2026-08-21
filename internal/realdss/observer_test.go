package realdss

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReferenceObserverUsesIndependentOAuthIdentity(t *testing.T) {
	const intentID = "10000000-0000-4000-8000-000000000001"
	client := &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := ""
		switch request.URL.Path {
		case "/token":
			query := request.URL.Query()
			if query.Get("scope") != "utm.strategic_coordination" || query.Get("intended_audience") != "localhost" ||
				query.Get("issuer") != "localhost" || query.Get("sub") != "uss-a" {
				t.Errorf("unexpected OAuth query: %s", request.URL.RawQuery)
			}
			body = `{"access_token":"observer-token"}`
		case "/dss/v1/operational_intent_references/" + intentID:
			if request.Header.Get("Authorization") != "Bearer observer-token" {
				t.Errorf("authorization = %q", request.Header.Get("Authorization"))
			}
			encoded, err := json.Marshal(map[string]any{
				"operational_intent_reference": map[string]any{
					"version": 2, "state": "Activated", "ovn": "ovn-2",
					"manager": "uss-a", "uss_base_url": "http://uss-a:8080",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			body = string(encoded)
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status),
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})}

	observer := &ReferenceObserver{
		dssBaseURL: "http://dss.test", tokenURL: "http://oauth.test/token", subject: "uss-a", http: client,
	}
	reference, err := observer.ObserveDSSReference(context.Background(), intentID)
	if err != nil {
		t.Fatal(err)
	}
	if !reference.Exists || reference.Version != 2 || reference.State != "Activated" || reference.OVN != "ovn-2" ||
		reference.Manager != "uss-a" || reference.USSBaseURL != "http://uss-a:8080" {
		t.Fatalf("reference = %#v", reference)
	}

	missing, err := observer.ObserveDSSReference(context.Background(), "30000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Exists {
		t.Fatalf("missing reference = %#v", missing)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
