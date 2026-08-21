//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Aero-Arc/aero-arc-test-harness/assertions"
	"github.com/Aero-Arc/aero-arc-test-harness/environments/docker"
	"github.com/Aero-Arc/aero-arc-test-harness/faults"
	fixturefaults "github.com/Aero-Arc/aero-arc-test-harness/faults/fixture"
	toxiproxyfaults "github.com/Aero-Arc/aero-arc-test-harness/faults/toxiproxy"
	"github.com/Aero-Arc/aero-arc-test-harness/internal/fixture"
	"github.com/Aero-Arc/aero-arc-test-harness/reports"
)

var (
	testStack *docker.Environment
	timeline  *reports.Timeline
)

func TestMain(m *testing.M) {
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	apiSource := os.Getenv("AERO_ARC_API_SOURCE")
	if apiSource == "" {
		apiSource = filepath.Join(root, "..", "aero-arc-api")
	}
	artifactDir := os.Getenv("AERO_ARC_E2E_ARTIFACT_DIR")
	if artifactDir == "" {
		artifactDir = filepath.Join(root, "artifacts", "manual")
	}
	seed, _ := strconv.ParseInt(os.Getenv("AERO_ARC_E2E_SEED"), 10, 64)
	if seed == 0 {
		seed = 1
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	timeline, err = reports.OpenTimeline(filepath.Join(artifactDir, "events.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	testStack, err = docker.Start(ctx, docker.Config{
		RootDir: root, APISource: apiSource, ArtifactDir: artifactDir, Seed: seed,
	})
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = timeline.Close()
		os.Exit(1)
	}
	code := m.Run()
	captureCtx, captureCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if code != 0 || os.Getenv("AERO_ARC_E2E_CAPTURE_ALL_LOGS") == "true" {
		_ = testStack.Capture(captureCtx)
	}
	_ = testStack.Close(captureCtx)
	captureCancel()
	_ = timeline.Close()
	os.Exit(code)
}

func TestFederation(t *testing.T) {
	t.Run("happy_path_activates_DSS_before_local", func(t *testing.T) {
		resetFederation(t)
		intentID := "11111111-1111-4111-8111-111111111111"
		createSubmittedIntent(t, intentID)
		publishAcceptedAndAwaitConfirmation(t, intentID)
		activateAndRequireLocalActive(t, intentID)
		assertActiveHasMatchingDSSActivation(t, intentID)
		assertDSSActivationPrecededLocalActive(t)
	})

	t.Run("lost_DSS_create_response_recovers_existing_reference", func(t *testing.T) {
		resetFederation(t)
		intentID := "22222222-2222-4222-8222-222222222222"
		createSubmittedIntent(t, intentID)
		dropNextDSSCreateResponseAfterCommit(t)
		coordination := publishAcceptedAndAwaitConfirmation(t, intentID)
		assertReconciliationRecoveredOVN(t, coordination)
		assertExactlyOneDSSReference(t, intentID)
		activateAndRequireLocalActive(t, intentID)
		assertActiveHasMatchingDSSActivation(t, intentID)
	})

	t.Run("stale_OVN_withdrawal_reads_and_retries", func(t *testing.T) {
		resetFederation(t)
		intentID := "33333333-3333-4333-8333-333333333333"
		createSubmittedIntent(t, intentID)
		publishAcceptedAndAwaitConfirmation(t, intentID)
		changeDSSOVNOutsideAeroArc(t, intentID)
		withdrawAndAwaitConvergence(t, intentID)
		assertStaleOVNWasRejectedBeforeRecovery(t, intentID)
	})

	t.Run("peer_500_fails_closed_then_recovers", func(t *testing.T) {
		resetFederation(t)
		intentID := "88888888-8888-4888-8888-888888888888"
		peerID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		createSubmittedIntent(t, intentID)
		createNonOverlappingPeerReference(t, peerID)
		clearPeerFault := armPeerDetailsFailures(t, 10)

		result := postExpect(t, "/api/v1/operational-intents/"+intentID+"/deconfliction/check", nil, http.StatusOK)
		if result["posture"] != "indeterminate" {
			t.Fatalf("deconfliction posture = %#v, want indeterminate", result["posture"])
		}
		assertDurableIndeterminateFinding(t, intentID)
		recordCheckpoint(t, "then", "peer failure produced an indeterminate posture", "pass", map[string]any{"posture": result["posture"]})

		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool { return value["sync_status"] == "retrying" })
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/activate", nil, http.StatusConflict)
		assertNoDSSReference(t, intentID)
		assertLocalStatus(t, intentID, "accepted")
		clearPeerFault()
		recordCheckpoint(t, "when", "peer details recovered", "pass", nil)

		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		recordCheckpoint(t, "then", "publication recovered after bounded peer failures", "pass", nil)
	})

	t.Run("DSS_latency_through_Toxiproxy_recovers", func(t *testing.T) {
		resetFederation(t)
		intentID := "99999999-9999-4999-8999-999999999999"
		createSubmittedIntent(t, intentID)
		injector := toxiproxyfaults.New(testStack.Toxiproxy, "dss")
		receipt, err := injector.Enable(context.Background(), faults.Fault{
			Name: "dss_downstream_latency", Kind: faults.Latency, Target: "dss", Duration: 1500 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		faultEnabled := true
		t.Cleanup(func() {
			if faultEnabled {
				_ = injector.Disable(context.Background(), receipt)
			}
		})
		recordCheckpoint(t, "given", "DSS responses exceed the API request timeout", "pass", map[string]any{"fault": receipt.Fault})

		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool { return value["sync_status"] == "retrying" })
		assertNoDSSReference(t, intentID)
		if err := injector.Disable(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		faultEnabled = false
		recordCheckpoint(t, "when", "DSS latency fault removed", "pass", nil)

		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		recordCheckpoint(t, "then", "publication recovered after network latency", "pass", nil)
	})

	t.Run("worker_death_waits_for_lease_then_recovers", func(t *testing.T) {
		resetFederation(t)
		intentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		createSubmittedIntent(t, intentID)
		dropNextDSSCreateResponseAfterCommit(t)
		armFixtureLatency(t, fixture.OperationGetReference, 10*time.Second, 1)
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitFixtureEvent(t, intentID, fixture.OperationCreateReference, 5*time.Second)
		leaseUntil := publicationLeaseUntil(t, intentID)

		apiRunning := true
		t.Cleanup(func() {
			if apiRunning {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := testStack.StartAPI(ctx); err != nil {
				t.Errorf("restore API after worker-death scenario: %v", err)
			}
		})
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := testStack.StopAPI(stopCtx); err != nil {
			stopCancel()
			t.Fatal(err)
		}
		stopCancel()
		apiRunning = false
		recordCheckpoint(t, "when", "API worker stopped after remote commit", "pass", map[string]any{"lease_until": leaseUntil})
		assertExactlyOneDSSReference(t, intentID)

		startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := testStack.StartAPI(startCtx); err != nil {
			startCancel()
			t.Fatal(err)
		}
		startCancel()
		apiRunning = true
		assertNoDSSMutationBefore(t, intentID, leaseUntil)
		coordination := awaitCoordinationWithin(t, intentID, 50*time.Second, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		if attempts, _ := coordination["attempt_count"].(float64); attempts < 2 {
			t.Fatalf("attempt_count = %v, want lease takeover attempt", coordination["attempt_count"])
		}
		assertTakeoverMutationsFollowLease(t, intentID, leaseUntil)
		assertExactlyOneDSSReference(t, intentID)
		recordCheckpoint(t, "then", "expired lease allowed idempotent worker takeover", "pass", map[string]any{
			"attempt_count": coordination["attempt_count"], "lease_until": leaseUntil,
		})
	})
}

// TestInvariantTripwires deliberately creates forbidden cross-system states.
// It passes only when the invariant checker detects each corruption.
func TestInvariantTripwires(t *testing.T) {
	t.Run("rejects_local_Active_without_DSS_Activated", func(t *testing.T) {
		resetFederation(t)
		intentID := "44444444-4444-4444-8444-444444444444"
		createSubmittedIntent(t, intentID)
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		mutateDatabase(t, `UPDATE operational_intents
			SET data = jsonb_set(data, '{status}', '"active"'::jsonb)
			WHERE id = $1`, intentID)
		result := snapshotInvariant(t, intentID, assertions.ActiveRequiresDSSActivated)
		assertTripwire(t, intentID, result)
	})

	t.Run("rejects_blocked_version_visible_in_DSS", func(t *testing.T) {
		resetFederation(t)
		intentID := "55555555-5555-4555-8555-555555555555"
		createSubmittedIntent(t, intentID)
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		mutateDatabase(t, `UPDATE operational_intent_publications
			SET sync_status = 'blocked' WHERE intent_id = $1`, intentID)
		result := snapshotInvariant(t, intentID, assertions.BlockedVersionHasNoDSSReference)
		assertTripwire(t, intentID, result)
	})
}

// TestFailureArtifact is an opt-in fire drill for the evidence pipeline. It
// intentionally fails and is excluded from the normal suite by its run filter.
func TestFailureArtifact(t *testing.T) {
	if os.Getenv("AERO_ARC_E2E_DEMONSTRATE_FAILURE") != "true" {
		t.Skip("set AERO_ARC_E2E_DEMONSTRATE_FAILURE=true to generate a known-bad report")
	}
	t.Run("local_Active_without_DSS_Activated", func(t *testing.T) {
		resetFederation(t)
		intentID := "66666666-6666-4666-8666-666666666666"
		createSubmittedIntent(t, intentID)
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		mutateDatabase(t, `UPDATE operational_intents
			SET data = jsonb_set(data, '{status}', '"active"'::jsonb)
			WHERE id = $1`, intentID)
		assertInvariant(t, intentID, snapshotInvariant(t, intentID, assertions.ActiveRequiresDSSActivated))
	})

	t.Run("blocked_version_visible_in_DSS", func(t *testing.T) {
		resetFederation(t)
		intentID := "77777777-7777-4777-8777-777777777777"
		createSubmittedIntent(t, intentID)
		postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
		awaitCoordination(t, intentID, func(value map[string]any) bool {
			return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted"
		})
		mutateDatabase(t, `UPDATE operational_intent_publications
			SET sync_status = 'blocked' WHERE intent_id = $1`, intentID)
		assertInvariant(t, intentID, snapshotInvariant(t, intentID, assertions.BlockedVersionHasNoDSSReference))
	})
}

func publishAcceptedAndAwaitConfirmation(t *testing.T, intentID string) map[string]any {
	t.Helper()
	postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
	coordination := awaitCoordination(t, intentID, func(value map[string]any) bool {
		return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted" && value["ovn"] != ""
	})
	recordCheckpoint(t, "when", "DSS publication confirmed Accepted", "pass", map[string]any{"ovn": coordination["ovn"]})
	return coordination
}

func activateAndRequireLocalActive(t *testing.T, intentID string) {
	t.Helper()
	active := postExpect(t, "/api/v1/operational-intents/"+intentID+"/activate", nil, http.StatusOK)
	if active["status"] != "active" {
		t.Fatalf("activation status = %#v", active["status"])
	}
}

func assertActiveHasMatchingDSSActivation(t *testing.T, intentID string) {
	t.Helper()
	assertInvariant(t, intentID, snapshotInvariant(t, intentID, assertions.ActiveRequiresDSSActivated))
}

func assertDSSActivationPrecededLocalActive(t *testing.T) {
	t.Helper()
	events := fixtureEvents(t)
	assertOperationOrder(t, events, fixture.OperationCreateReference, fixture.OperationUpdateReference)
	recordCheckpoint(t, "then", "DSS activation preceded local Active", "pass", map[string]any{"dss_events": len(events)})
}

func dropNextDSSCreateResponseAfterCommit(t *testing.T) {
	t.Helper()
	injector := fixturefaults.New(testStack.FixtureControlURL)
	receipt, err := injector.Enable(context.Background(), faults.Fault{
		Name: "drop_dss_create_response_after_commit", Kind: faults.DropResponseAfterCommit,
		Target: "dss", Operation: fixture.OperationCreateReference, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = injector.Disable(context.Background(), receipt) })
	recordCheckpoint(t, "given", "DSS create response will be dropped after commit", "pass", map[string]any{"fault": receipt.Fault})
}

func armPeerDetailsFailures(t *testing.T, count int) func() {
	t.Helper()
	injector := fixturefaults.New(testStack.FixtureControlURL)
	receipt, err := injector.Enable(context.Background(), faults.Fault{
		Name: "peer_details_500", Kind: faults.HTTPStatus, Target: "peer",
		Operation: fixture.OperationPeerDetails, Count: count,
		Attributes: map[string]any{"status": http.StatusInternalServerError},
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	disable := func() {
		if !enabled {
			return
		}
		if err := injector.Disable(context.Background(), receipt); err != nil {
			t.Errorf("clear peer details fault: %v", err)
			return
		}
		enabled = false
	}
	t.Cleanup(disable)
	recordCheckpoint(t, "given", "peer details return bounded 500 responses", "pass", map[string]any{"fault": receipt.Fault})
	return disable
}

func armFixtureLatency(t *testing.T, operation string, latency time.Duration, count int) {
	t.Helper()
	injector := fixturefaults.New(testStack.FixtureControlURL)
	receipt, err := injector.Enable(context.Background(), faults.Fault{
		Name: "delay_" + strings.ReplaceAll(operation, ".", "_"), Kind: faults.Latency,
		Target: "fixture", Operation: operation, Count: count, Duration: latency,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = injector.Disable(context.Background(), receipt) })
}

func createNonOverlappingPeerReference(t *testing.T, intentID string) {
	t.Helper()
	start := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)
	end := start.Add(40 * time.Minute)
	extent := map[string]any{
		"time_start": map[string]any{"format": "RFC3339", "value": start},
		"time_end":   map[string]any{"format": "RFC3339", "value": end},
		"volume": map[string]any{
			"outline_polygon": map[string]any{"vertices": []map[string]any{
				{"lat": 32.0, "lng": -96.0}, {"lat": 32.0, "lng": -95.9},
				{"lat": 32.1, "lng": -95.9}, {"lat": 32.1, "lng": -96.0},
			}},
			"altitude_lower": map[string]any{"value": 30, "reference": "W84", "units": "M"},
			"altitude_upper": map[string]any{"value": 90, "reference": "W84", "units": "M"},
		},
	}
	requestExpect(t, http.MethodPut,
		testStack.FixtureControlURL+"/dss/v1/operational_intent_references/"+intentID,
		map[string]any{"state": "Accepted", "uss_base_url": "http://fixture:8081", "extents": []any{extent}},
		http.StatusCreated,
	)
	recordCheckpoint(t, "given", "non-overlapping peer reference exists", "pass", map[string]any{"peer_intent_id": intentID})
}

func assertReconciliationRecoveredOVN(t *testing.T, coordination map[string]any) {
	t.Helper()
	if attempts, _ := coordination["attempt_count"].(float64); attempts < 2 {
		t.Fatalf("attempt_count = %v, want recovery attempt", coordination["attempt_count"])
	}
	recordCheckpoint(t, "then", "reconciliation recovered committed reference", "pass", map[string]any{
		"attempt_count": coordination["attempt_count"], "ovn": coordination["ovn"],
	})
}

func assertExactlyOneDSSReference(t *testing.T, intentID string) {
	t.Helper()
	count := 0
	for _, reference := range fixtureReferences(t) {
		if reference["id"] == intentID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("DSS reference count = %d, want exactly one", count)
	}
	recordCheckpoint(t, "then", "no duplicate DSS reference", "pass", map[string]any{"reference_count": count})
}

func changeDSSOVNOutsideAeroArc(t *testing.T, intentID string) {
	t.Helper()
	fixturePostExpect(t, "/control/intents/"+intentID+"/rotate-ovn", nil, http.StatusOK)
	recordCheckpoint(t, "given", "DSS OVN changed outside Aero Arc", "pass", nil)
}

func withdrawAndAwaitConvergence(t *testing.T, intentID string) {
	t.Helper()
	postExpect(t, "/api/v1/operational-intents/"+intentID+"/cancel", nil, http.StatusOK)
	awaitCoordination(t, intentID, func(value map[string]any) bool {
		return value["sync_status"] == "withdrawn" && value["desired_state"] == "Withdrawn"
	})
}

func assertStaleOVNWasRejectedBeforeRecovery(t *testing.T, intentID string) {
	t.Helper()
	for _, reference := range fixtureReferences(t) {
		if reference["id"] == intentID {
			t.Fatalf("reference still exists after withdrawal: %#v", reference)
		}
	}
	deleteConflicts := 0
	for _, event := range fixtureEvents(t) {
		if event.Operation == fixture.OperationDeleteReference && event.Status == http.StatusConflict {
			deleteConflicts++
		}
	}
	if deleteConflicts == 0 {
		t.Fatal("expected a stale-OVN delete conflict before recovery")
	}
	recordCheckpoint(t, "then", "stale OVN was rejected before withdrawal recovered", "pass", map[string]any{
		"delete_conflicts": deleteConflicts, "reference_exists": false,
	})
}

func mutateDatabase(t *testing.T, statement, intentID string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testStack.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(), statement, intentID); err != nil {
		t.Fatalf("apply intentional invariant corruption: %v", err)
	}
	recordCheckpoint(t, "when", "intentional forbidden state installed", "pass", map[string]any{"intent_id": intentID})
}

func snapshotInvariant(t *testing.T, intentID string, check func(assertions.Snapshot) assertions.Result) assertions.Result {
	t.Helper()
	probe := newProbe(t)
	defer probe.Close()
	snapshot, err := probe.Snapshot(context.Background(), intentID)
	if err != nil {
		t.Fatal(err)
	}
	return check(snapshot)
}

func assertTripwire(t *testing.T, intentID string, result assertions.Result) {
	t.Helper()
	if result.Passed {
		recordCheckpoint(t, "then", result.Name+" tripwire", "fail", map[string]any{"intent_id": intentID})
		t.Fatalf("tripwire failed to reject forbidden state: %#v", result)
	}
	recordCheckpoint(t, "then", result.Name+" tripwire", "pass", map[string]any{
		"intent_id": intentID, "detected_violation": result.Message, "observed": result.Observed,
	})
}

func createSubmittedIntent(t *testing.T, intentID string) {
	t.Helper()
	start := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)
	end := start.Add(40 * time.Minute)
	postExpect(t, "/api/v1/operational-intents", map[string]any{
		"id": intentID, "aircraft_id": "aircraft-hawk-2", "name": "E2E federation operation",
		"summary": "Deterministic federation harness operation", "planned_start_at": start, "planned_end_at": end,
	}, http.StatusCreated)
	postExpect(t, "/api/v1/operational-intents/"+intentID+"/volumes", map[string]any{
		"id": intentID + "-volume", "sequence": 1,
		"geojson":        `{"type":"Polygon","coordinates":[[[-97.760,30.250],[-97.750,30.250],[-97.750,30.260],[-97.760,30.260],[-97.760,30.250]]]}`,
		"min_altitude_m": 30, "max_altitude_m": 90, "altitude_ref": "wgs84",
		"starts_at": start.Add(time.Minute), "ends_at": end.Add(-time.Minute), "volume_type": "route",
	}, http.StatusCreated)
	postExpect(t, "/api/v1/operational-intents/"+intentID+"/submit", nil, http.StatusOK)
}

func awaitCoordination(t *testing.T, intentID string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	return awaitCoordinationWithin(t, intentID, 20*time.Second, ready)
}

func awaitCoordinationWithin(t *testing.T, intentID string, timeout time.Duration, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	delay := 50 * time.Millisecond
	var last map[string]any
	lastTransition := ""
	for time.Now().Before(deadline) {
		status, value := getJSON(t, testStack.APIBaseURL+"/api/v1/operational-intents/"+intentID+"/coordination")
		if status == http.StatusOK {
			last = value
			transition := fmt.Sprintf("%v|%v|%v|%v|%v", value["sync_status"], value["desired_state"], value["confirmed_state"], value["attempt_count"], value["last_error"])
			if transition != lastTransition {
				recordCheckpoint(t, "observe", "coordination state transition", "observed", map[string]any{
					"sync_status": value["sync_status"], "desired_state": value["desired_state"],
					"confirmed_state": value["confirmed_state"], "attempt_count": value["attempt_count"],
					"last_error": value["last_error"],
				})
				lastTransition = transition
			}
			if ready(value) {
				return value
			}
		}
		time.Sleep(delay)
		if delay < 500*time.Millisecond {
			delay = time.Duration(float64(delay) * 1.5)
			if delay > 500*time.Millisecond {
				delay = 500 * time.Millisecond
			}
		}
	}
	t.Fatalf("coordination did not converge: %#v", last)
	return nil
}

func assertNoDSSReference(t *testing.T, intentID string) {
	t.Helper()
	for _, reference := range fixtureReferences(t) {
		if reference["id"] == intentID {
			t.Fatalf("DSS reference exists for %s while publication must fail closed", intentID)
		}
	}
	recordCheckpoint(t, "then", "failed coordination did not publish a DSS reference", "pass", map[string]any{"intent_id": intentID})
}

func assertLocalStatus(t *testing.T, intentID, expected string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testStack.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT data->>'status' FROM operational_intents
		WHERE id = $1 ORDER BY version DESC LIMIT 1`, intentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != expected {
		t.Fatalf("local status = %q, want %q", status, expected)
	}
	recordCheckpoint(t, "then", "local workflow remained fail closed", "pass", map[string]any{"local_status": status})
}

func assertDurableIndeterminateFinding(t *testing.T, intentID string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testStack.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var findings int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM conflict_findings
		WHERE intent_id = $1 AND data->>'status' = 'indeterminate'`, intentID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if findings == 0 {
		t.Fatal("peer failure did not produce a durable indeterminate finding")
	}
	peerFailures := 0
	for _, event := range fixtureEvents(t) {
		if event.Operation == fixture.OperationPeerDetails && event.Status == http.StatusInternalServerError {
			peerFailures++
		}
	}
	if peerFailures == 0 {
		t.Fatal("fixture journal did not record the peer failure")
	}
	recordCheckpoint(t, "then", "durable finding matches peer failure journal", "pass", map[string]any{
		"indeterminate_findings": findings, "peer_500_events": peerFailures,
	})
}

func publicationLeaseUntil(t *testing.T, intentID string) time.Time {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testStack.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var leaseUntil *time.Time
	var syncStatus string
	if err := pool.QueryRow(context.Background(), `
		SELECT lease_until, sync_status FROM operational_intent_publications
		WHERE intent_id = $1`, intentID).Scan(&leaseUntil, &syncStatus); err != nil {
		t.Fatal(err)
	}
	if leaseUntil == nil || syncStatus != "processing" {
		t.Fatalf("publication lease = %v status = %q, want active processing lease", leaseUntil, syncStatus)
	}
	return leaseUntil.UTC()
}

func awaitFixtureEvent(t *testing.T, intentID, operation string, timeout time.Duration) fixture.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	delay := 25 * time.Millisecond
	for time.Now().Before(deadline) {
		for _, event := range fixtureEvents(t) {
			if event.IntentID == intentID && event.Operation == operation {
				recordCheckpoint(t, "observe", "fixture operation observed", "observed", map[string]any{
					"operation": operation, "sequence": event.Sequence, "status": event.Status,
				})
				return event
			}
		}
		time.Sleep(delay)
		if delay < 250*time.Millisecond {
			delay *= 2
			if delay > 250*time.Millisecond {
				delay = 250 * time.Millisecond
			}
		}
	}
	t.Fatalf("fixture operation %s for %s was not observed", operation, intentID)
	return fixture.Event{}
}

func assertNoDSSMutationBefore(t *testing.T, intentID string, leaseUntil time.Time) {
	t.Helper()
	observationEnd := time.Now().Add(2 * time.Second)
	if leaseUntil.Before(observationEnd) {
		observationEnd = leaseUntil
	}
	delay := 50 * time.Millisecond
	for time.Now().Before(observationEnd) {
		events := dssMutationEvents(t, intentID)
		for _, event := range events[1:] {
			if event.At.Before(leaseUntil) {
				t.Fatalf("DSS mutation %s at %s occurred before lease expiry %s", event.Operation, event.At, leaseUntil)
			}
		}
		time.Sleep(delay)
		if delay < 500*time.Millisecond {
			delay *= 2
			if delay > 500*time.Millisecond {
				delay = 500 * time.Millisecond
			}
		}
	}
	recordCheckpoint(t, "then", "replacement worker respected the live lease", "pass", map[string]any{"lease_until": leaseUntil})
}

func assertTakeoverMutationsFollowLease(t *testing.T, intentID string, leaseUntil time.Time) {
	t.Helper()
	events := dssMutationEvents(t, intentID)
	if len(events) < 2 {
		t.Fatalf("DSS mutation events = %d, want recovery activity after worker restart", len(events))
	}
	for _, event := range events[1:] {
		if event.At.Before(leaseUntil) {
			t.Fatalf("takeover mutation %s at %s preceded lease expiry %s", event.Operation, event.At, leaseUntil)
		}
	}
}

func dssMutationEvents(t *testing.T, intentID string) []fixture.Event {
	t.Helper()
	var result []fixture.Event
	for _, event := range fixtureEvents(t) {
		if event.IntentID != intentID {
			continue
		}
		switch event.Operation {
		case fixture.OperationCreateReference, fixture.OperationUpdateReference, fixture.OperationDeleteReference:
			result = append(result, event)
		}
	}
	return result
}

func newProbe(t *testing.T) *assertions.Probe {
	t.Helper()
	probe, err := assertions.NewProbe(context.Background(), testStack.DatabaseURL, testStack.FixtureControlURL)
	if err != nil {
		t.Fatal(err)
	}
	return probe
}

func assertInvariant(t *testing.T, intentID string, result assertions.Result) {
	t.Helper()
	status := "pass"
	if !result.Passed {
		status = "fail"
	}
	_ = timeline.Record(reports.TimelineEvent{
		Scenario: t.Name(), Phase: "then", Name: result.Name, Status: status,
		Details: map[string]any{"intent_id": intentID, "message": result.Message, "observed": result.Observed},
	})
	if !result.Passed {
		t.Fatal(result.Message)
	}
}

func recordCheckpoint(t *testing.T, phase, name, status string, details map[string]any) {
	t.Helper()
	if err := timeline.Record(reports.TimelineEvent{
		Scenario: t.Name(), Phase: phase, Name: name, Status: status, Details: details,
	}); err != nil {
		t.Fatalf("record timeline checkpoint: %v", err)
	}
}

func resetFederation(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := testStack.Reset(ctx); err != nil {
		t.Fatalf("reset federation environment: %v", err)
	}
	_ = timeline.Record(reports.TimelineEvent{
		Scenario: t.Name(), Phase: "given", Name: "isolated federation state", Status: "pass",
	})
}

func fixtureEvents(t *testing.T) []fixture.Event {
	t.Helper()
	response, err := http.Get(testStack.FixtureControlURL + "/control/events")
	if err != nil {
		t.Fatal(err)
	}
	events, err := fixture.DecodeEvents(response)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func fixtureReferences(t *testing.T) []map[string]any {
	t.Helper()
	status, body := getJSON(t, testStack.FixtureControlURL+"/control/state")
	if status != http.StatusOK {
		t.Fatalf("fixture state status = %d", status)
	}
	values, _ := body["operational_intent_references"].([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func assertOperationOrder(t *testing.T, events []fixture.Event, first, second string) {
	t.Helper()
	firstSequence := uint64(0)
	secondSequence := uint64(0)
	for _, event := range events {
		if event.Operation == first && firstSequence == 0 {
			firstSequence = event.Sequence
		}
		if event.Operation == second && secondSequence == 0 {
			secondSequence = event.Sequence
		}
	}
	if firstSequence == 0 || secondSequence == 0 || firstSequence >= secondSequence {
		t.Fatalf("operation order %s(%d) -> %s(%d) not observed", first, firstSequence, second, secondSequence)
	}
}

func postExpect(t *testing.T, path string, body any, expected int) map[string]any {
	t.Helper()
	return requestExpect(t, http.MethodPost, testStack.APIBaseURL+path, body, expected)
}

func fixturePostExpect(t *testing.T, path string, body any, expected int) map[string]any {
	t.Helper()
	return requestExpect(t, http.MethodPost, testStack.FixtureControlURL+path, body, expected)
}

func requestExpect(t *testing.T, method, url string, body any, expected int) map[string]any {
	t.Helper()
	var payload io.Reader
	if body != nil {
		var buffer bytes.Buffer
		if err := json.NewEncoder(&buffer).Encode(body); err != nil {
			t.Fatal(err)
		}
		payload = &buffer
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != expected {
		t.Fatalf("%s %s status = %d, want %d: %s", method, url, response.StatusCode, expected, strings.TrimSpace(string(data)))
	}
	if len(data) == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s %s: %v: %s", method, url, err, data)
	}
	return value
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value map[string]any
	_ = json.NewDecoder(response.Body).Decode(&value)
	return response.StatusCode, value
}
