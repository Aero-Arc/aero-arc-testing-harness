// Package faults defines behavior-oriented failures independently of the
// mechanism used to inject them.
package faults

import (
	"context"
	"fmt"
	"time"
)

// Kind identifies an observable failure behavior, not an implementation tool.
type Kind string

const (
	DropResponseAfterCommit Kind = "drop_response_after_commit"
	HTTPStatus              Kind = "http_status"
	MalformedResponse       Kind = "malformed_response"
	Latency                 Kind = "latency"
	Timeout                 Kind = "timeout"
	ConnectionReset         Kind = "connection_reset"
	ContainerKill           Kind = "container_kill"
)

// Fault is a bounded, named failure injected at a scenario checkpoint.
type Fault struct {
	Name       string         `json:"name"`
	Kind       Kind           `json:"kind"`
	Target     string         `json:"target"`
	Operation  string         `json:"operation,omitempty"`
	Count      int            `json:"count,omitempty"`
	Duration   time.Duration  `json:"duration,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Validate rejects unbounded or ambiguous fault declarations.
func (fault Fault) Validate() error {
	if fault.Name == "" || fault.Kind == "" || fault.Target == "" {
		return fmt.Errorf("fault name, kind, and target are required")
	}
	if fault.Count < 0 || fault.Duration < 0 {
		return fmt.Errorf("fault count and duration cannot be negative")
	}
	if fault.Count == 0 && fault.Duration == 0 && fault.Kind != ContainerKill {
		return fmt.Errorf("fault %q must be bounded by count or duration", fault.Name)
	}
	return nil
}

// Receipt identifies one enabled fault so cleanup can disable the exact
// mechanism even when a scenario exits early.
type Receipt struct {
	Fault      Fault     `json:"fault"`
	Injector   string    `json:"injector"`
	ExternalID string    `json:"external_id,omitempty"`
	EnabledAt  time.Time `json:"enabled_at"`
}

// Injector applies and removes a behavioral fault.
type Injector interface {
	Enable(context.Context, Fault) (Receipt, error)
	Disable(context.Context, Receipt) error
}
