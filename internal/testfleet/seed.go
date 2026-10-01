// Package testfleet creates only the fleet prerequisite owned by federation tests.
package testfleet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Seed creates the harness aircraft and installed battery through the API without seeding active flights.
// Parameters: ctx bounds setup; baseURL identifies a test-owned API instance.
// Returns: a transport or status error on failure; no flight or execution evidence
// is created, and an existing aircraft conflict is not silently accepted.
func Seed(ctx context.Context, baseURL string) error {
	for _, fixture := range []struct{ path, body string }{
		{"/api/v1/aircraft", `{"id":"aircraft-hawk-2","operator_id":"operator-demo","agent_id":"agent-hawk-2","name":"Federation test aircraft","status":"active","acceptance_status":"accepted","remote_id_status":"broadcasting"}`},
		{"/api/v1/batteries", `{"id":"battery-hawk-test","operator_id":"operator-demo","state_of_health":95,"status":"current"}`},
		{"/api/v1/aircraft/aircraft-hawk-2/battery-installations", `{"id":"install-hawk-test","battery_id":"battery-hawk-test"}`},
	} {
		if err := postFixture(ctx, baseURL+fixture.path, fixture.body); err != nil {
			return err
		}
	}
	return nil
}

func postFixture(ctx context.Context, url, body string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("seed federation fleet: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("seed federation fleet %s: HTTP %d: %s", url, response.StatusCode, body)
	}
	return nil
}
