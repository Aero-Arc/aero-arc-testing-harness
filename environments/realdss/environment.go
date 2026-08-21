// Package realdss exposes the protocol-fidelity Docker environment containing
// a real InterUSS DSS and two independent Aero Arc USS instances.
package realdss

import (
	"context"

	stack "github.com/Aero-Arc/aero-arc-test-harness/internal/realdss"
)

type Config = stack.Config
type Environment = stack.Environment
type Participant = stack.Participant
type ReferenceObserver = stack.ReferenceObserver

func Start(ctx context.Context, config Config) (*Environment, error) {
	return stack.Start(ctx, config)
}
