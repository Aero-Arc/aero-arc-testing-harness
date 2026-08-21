// Package toxiproxy adapts behavior-oriented faults to Toxiproxy toxics.
package toxiproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/Aero-Arc/aero-arc-test-harness/faults"
	toxiclient "github.com/Aero-Arc/aero-arc-test-harness/internal/toxiproxy"
)

// Injector applies network faults to a configured Toxiproxy proxy.
type Injector struct {
	client *toxiclient.Client
	proxy  string
	now    func() time.Time
}

// New returns a Toxiproxy fault injector.
func New(client *toxiclient.Client, proxy string) *Injector {
	return &Injector{client: client, proxy: proxy, now: time.Now}
}

// Enable maps a behavioral network fault to one downstream toxic.
func (injector *Injector) Enable(ctx context.Context, fault faults.Fault) (faults.Receipt, error) {
	if err := fault.Validate(); err != nil {
		return faults.Receipt{}, err
	}
	toxic := toxiclient.Toxic{Name: fault.Name, Stream: "downstream", Toxicity: 1, Attributes: map[string]any{}}
	switch fault.Kind {
	case faults.Latency:
		toxic.Type = "latency"
		toxic.Attributes["latency"] = durationMillis(fault.Duration)
		toxic.Attributes["jitter"] = intAttribute(fault.Attributes, "jitter_ms")
	case faults.Timeout:
		toxic.Type = "timeout"
		toxic.Attributes["timeout"] = durationMillis(fault.Duration)
	case faults.ConnectionReset:
		toxic.Type = "reset_peer"
		toxic.Attributes["timeout"] = durationMillis(fault.Duration)
	default:
		return faults.Receipt{}, fmt.Errorf("fault kind %q requires a non-Toxiproxy injector", fault.Kind)
	}
	if err := injector.client.AddToxic(ctx, injector.proxy, toxic); err != nil {
		return faults.Receipt{}, err
	}
	return faults.Receipt{Fault: fault, Injector: "toxiproxy", ExternalID: toxic.Name, EnabledAt: injector.now().UTC()}, nil
}

// Disable removes the toxic identified by the receipt.
func (injector *Injector) Disable(ctx context.Context, receipt faults.Receipt) error {
	return injector.client.RemoveToxic(ctx, injector.proxy, receipt.ExternalID)
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 1
	}
	return duration.Milliseconds()
}

func intAttribute(attributes map[string]any, key string) int {
	if attributes == nil {
		return 0
	}
	value, ok := attributes[key]
	if !ok {
		return 0
	}
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	default:
		return 0
	}
}
