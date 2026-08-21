// Package fixture adapts behavioral protocol faults to the deterministic
// DSS/peer fixture control plane.
package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Aero-Arc/aero-arc-test-harness/faults"
	fixtureapi "github.com/Aero-Arc/aero-arc-test-harness/internal/fixture"
)

// Injector controls bounded fixture faults over HTTP.
type Injector struct {
	baseURL string
	client  *http.Client
	now     func() time.Time
}

// New returns a deterministic protocol-fault injector.
func New(baseURL string) *Injector {
	return &Injector{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
		now:     time.Now,
	}
}

// Enable arms one bounded fault for the named fixture operation.
func (injector *Injector) Enable(ctx context.Context, fault faults.Fault) (faults.Receipt, error) {
	if err := fault.Validate(); err != nil {
		return faults.Receipt{}, err
	}
	if fault.Operation == "" {
		return faults.Receipt{}, fmt.Errorf("fixture fault %q requires an operation", fault.Name)
	}
	plan := fixtureapi.FaultPlan{Operation: fault.Operation, Times: fault.Count}
	if plan.Times == 0 {
		plan.Times = 1
	}
	switch fault.Kind {
	case faults.DropResponseAfterCommit:
		plan.CloseAfterCommit = true
	case faults.HTTPStatus:
		plan.Status = attributeInt(fault.Attributes, "status", http.StatusInternalServerError)
	case faults.MalformedResponse:
		plan.MalformedJSON = true
	case faults.Latency:
		plan.DelayMillis = int(fault.Duration.Milliseconds())
	default:
		return faults.Receipt{}, fmt.Errorf("fault kind %q requires another injector", fault.Kind)
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(plan); err != nil {
		return faults.Receipt{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, injector.baseURL+"/control/faults", &body)
	if err != nil {
		return faults.Receipt{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := injector.client.Do(request)
	if err != nil {
		return faults.Receipt{}, fmt.Errorf("arm fixture fault: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return faults.Receipt{}, fmt.Errorf("arm fixture fault returned %d", response.StatusCode)
	}
	return faults.Receipt{Fault: fault, Injector: "fixture", ExternalID: fault.Operation, EnabledAt: injector.now().UTC()}, nil
}

// Disable resets pending fixture faults. Bounded faults normally self-remove;
// this cleanup handles scenarios that exit before consuming them.
func (injector *Injector) Disable(ctx context.Context, _ faults.Receipt) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, injector.baseURL+"/control/faults/clear", nil)
	if err != nil {
		return err
	}
	response, err := injector.client.Do(request)
	if err != nil {
		return fmt.Errorf("clear fixture faults: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("clear fixture faults returned %d", response.StatusCode)
	}
	return nil
}

func attributeInt(attributes map[string]any, key string, fallback int) int {
	if attributes == nil {
		return fallback
	}
	switch value := attributes[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return fallback
	}
}
