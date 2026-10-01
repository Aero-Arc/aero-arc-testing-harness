//go:build e2e && !realdss

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReset guards the scenario boundary against new foreign-key dependents.
// It uses the real API schema and isolated PostGIS provisioned by TestMain.
func TestReset(t *testing.T) {
	resetFederation(t)
	intentID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	createSubmittedIntent(t, intentID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, testStack.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var aircraftBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM aircraft`).Scan(&aircraftBefore); err != nil {
		t.Fatal(err)
	}
	if aircraftBefore == 0 {
		t.Fatal("expected seeded aircraft before reset")
	}
	// Populate both direct and transitive dependents without scheduling any
	// background publication or deployment work.
	for _, statement := range []string{
		`INSERT INTO flight_records
			(id, operator_id, aircraft_id, intent_id, intent_version, status, started_at, data)
			SELECT 'reset-flight', 'reset-operator', aircraft_id, id, version, 'completed', now(), '{}'
			FROM operational_intents WHERE id = $1 ORDER BY version DESC LIMIT 1`,
		`INSERT INTO missions
			(id, operator_id, flight_id, aircraft_id, intent_id, intent_version, version,
			source_format, source_sha256, mission_digest, idempotency_key, idempotency_request_hash, created_at, data)
			SELECT 'reset-mission', operator_id, id, aircraft_id, intent_id, intent_version, 1,
			'waypoints', repeat('0', 64), repeat('0', 64), 'reset-mission-key', repeat('0', 64), now(), '{}'
			FROM flight_records WHERE intent_id = $1`,
		`INSERT INTO mission_items (mission_id, sequence, data)
			SELECT id, 0, '{}' FROM missions WHERE intent_id = $1`,
		`INSERT INTO mission_deployments
			(id, flight_id, mission_id, idempotency_key, idempotency_request_hash, status, created_at, updated_at, data)
			SELECT 'reset-deployment', flight_id, id, 'reset-deployment-key', repeat('0', 64), 'completed', now(), now(), '{}'
			FROM missions WHERE intent_id = $1`,
		`INSERT INTO commands(id,operator_id,aircraft_id,flight_id,idempotency_key,request_hash,digest,payload,data,state,expires_at)
          SELECT 'reset-command',operator_id,aircraft_id,id,'reset-command-key','hash','digest','payload','{}','rejected',now() FROM flight_records WHERE intent_id=$1`,
		`INSERT INTO command_events(event_id,command_id,stage,occurred_at,source,message)
          SELECT 'reset-event','reset-command','rejected',now(),'test','test' WHERE EXISTS(SELECT 1 FROM flight_records WHERE intent_id=$1)`,
		`INSERT INTO command_outbox(command_id,done) SELECT 'reset-command',true WHERE EXISTS(SELECT 1 FROM flight_records WHERE intent_id=$1)`,
		`INSERT INTO command_attempts(command_id,attempt) SELECT 'reset-command',1 WHERE EXISTS(SELECT 1 FROM flight_records WHERE intent_id=$1)`,
		`INSERT INTO flight_completions(event_id,flight_id,digest,payload,state)
          SELECT 'reset-completion',id,'digest','payload','complete' FROM flight_records WHERE intent_id=$1`,
		`INSERT INTO flight_finalized_outbox(event_id,flight_id) SELECT 'reset-completion',id FROM flight_records WHERE intent_id=$1`,
	} {
		result, err := pool.Exec(ctx, statement, intentID)
		if err != nil {
			t.Fatalf("seed reset dependent: %v", err)
		}
		if result.RowsAffected() != 1 {
			t.Fatalf("seed reset dependent affected %d rows, want 1", result.RowsAffected())
		}
	}

	// Verify the populated reset and a second, already-empty reset. Explicit
	// table coverage keeps the test from silently accepting partial cleanup.
	for attempt := 1; attempt <= 2; attempt++ {
		if err := testStack.Reset(ctx); err != nil {
			t.Fatalf("reset attempt %d: %v", attempt, err)
		}
		for _, table := range []string{
			"commands", "command_events", "command_outbox", "command_attempts", "flight_completions", "flight_finalized_outbox",
			"mission_deployments", "mission_items", "missions", "flight_records",
			"received_peer_notifications", "peer_notifications", "operational_intent_publications",
			"conflict_findings", "operational_volumes", "operational_intents",
		} {
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("reset attempt %d left %d rows in %s", attempt, count, table)
			}
		}
		var aircraftAfter int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM aircraft`).Scan(&aircraftAfter); err != nil {
			t.Fatal(err)
		}
		if aircraftAfter != aircraftBefore {
			t.Errorf("reset attempt %d changed aircraft count from %d to %d", attempt, aircraftBefore, aircraftAfter)
		}
	}
	recordCheckpoint(t, "then", "reset cleared flight and mission dependents and preserved aircraft", "pass", nil)
}
