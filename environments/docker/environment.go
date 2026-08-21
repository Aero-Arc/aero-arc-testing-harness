// Package docker exposes the fast Testcontainers environment used for PR and
// local federation scenarios.
package docker

import (
	"context"

	"github.com/Aero-Arc/aero-arc-test-harness/internal/stack"
)

// Config configures one isolated Docker environment.
type Config = stack.Config

// Environment contains the live service endpoints and cleanup behavior.
type Environment = stack.Stack

// Start creates a full Docker federation using Testcontainers.
func Start(ctx context.Context, config Config) (*Environment, error) {
	return stack.Start(ctx, config)
}
