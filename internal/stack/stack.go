package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	toxiclient "github.com/Aero-Arc/aero-arc-test-harness/internal/toxiproxy"
)

const (
	postGISImage   = "postgis/postgis:14-3.5-alpine"
	toxiproxyImage = "ghcr.io/shopify/toxiproxy:2.12.0"
	proxyName      = "dss"
)

// Config identifies source trees, artifacts, and the deterministic run seed.
type Config struct {
	RootDir      string
	APISource    string
	ArtifactDir  string
	Seed         int64
	StartupLimit time.Duration
}

// Reset restores every stateful authority to a clean scenario boundary. It is
// intentionally owned by the environment so scenarios do not know database
// table names or fixture control endpoints.
func (stack *Stack) Reset(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, stack.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open reset database connection: %w", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		TRUNCATE received_peer_notifications, peer_notifications,
		operational_intent_publications, conflict_findings,
		operational_volumes, operational_intents`); err != nil {
		return fmt.Errorf("reset Aero Arc state: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, stack.FixtureControlURL+"/control/reset", nil)
	if err != nil {
		return fmt.Errorf("create fixture reset request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("reset DSS fixture: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reset DSS fixture returned %d", response.StatusCode)
	}
	return nil
}

// Stack is one isolated federation environment.
type Stack struct {
	RunID             string
	Seed              int64
	ArtifactDir       string
	APIBaseURL        string
	FixtureControlURL string
	DatabaseURL       string
	ToxiproxyURL      string
	Toxiproxy         *toxiclient.Client

	network    *testcontainers.DockerNetwork
	containers map[string]testcontainers.Container
	rootDir    string
}

// Start creates the complete Docker federation and returns only after every
// service's semantic readiness endpoint is healthy.
func Start(ctx context.Context, config Config) (_ *Stack, err error) {
	if config.StartupLimit <= 0 {
		config.StartupLimit = 4 * time.Minute
	}
	if config.RootDir == "" || config.APISource == "" || config.ArtifactDir == "" {
		return nil, fmt.Errorf("root, API source, and artifact directories are required")
	}
	for _, required := range []string{
		filepath.Join(config.RootDir, "Dockerfile.fixture"),
		filepath.Join(config.APISource, "Dockerfile"),
		filepath.Join(config.APISource, "go.mod"),
		filepath.Join(config.RootDir, "testdata", "keys", "uss-auth-public.pem"),
	} {
		if _, statErr := os.Stat(required); statErr != nil {
			return nil, fmt.Errorf("required harness input %s: %w", required, statErr)
		}
	}
	runID := fmt.Sprintf("e2e-%d-%d", time.Now().UTC().Unix(), config.Seed)
	stack := &Stack{
		RunID: runID, Seed: config.Seed, ArtifactDir: config.ArtifactDir,
		containers: make(map[string]testcontainers.Container), rootDir: config.RootDir,
	}
	defer func() {
		if err != nil {
			_ = stack.Capture(ctx)
			_ = stack.Close(context.Background())
		}
	}()

	stack.network, err = network.New(ctx, network.WithLabels(map[string]string{"aero-arc.io/test-run": runID}))
	if err != nil {
		return nil, fmt.Errorf("create test network: %w", err)
	}

	labels := map[string]string{"aero-arc.io/test-run": runID, "aero-arc.io/seed": fmt.Sprint(config.Seed)}
	postGIS, startErr := startContainer(ctx, stack.network, "postgis", testcontainers.ContainerRequest{
		Image:        postGISImage,
		Env:          map[string]string{"POSTGRES_DB": "aero_arc", "POSTGRES_USER": "aero_arc", "POSTGRES_PASSWORD": "aero_arc_test"},
		ExposedPorts: []string{"5432/tcp"}, Labels: labels,
		WaitingFor: wait.ForAll(
			wait.ForLog("PostgreSQL init process complete; ready for start up."),
			wait.ForExec([]string{"pg_isready", "-U", "aero_arc", "-d", "aero_arc"}),
		).WithDeadline(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start PostGIS: %w", startErr)
	}
	stack.containers["postgis"] = postGIS

	fixture, startErr := startContainer(ctx, stack.network, "fixture", testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: config.RootDir, Dockerfile: "Dockerfile.fixture", Repo: "aero-arc-fault-fixture", Tag: "e2e", KeepImage: true,
		},
		Cmd:          []string{"-peer-base-url", "http://fixture:8081", "-subscriber-url", "http://fixture:8081"},
		ExposedPorts: []string{"8081/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/healthz").WithPort("8081/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start DSS/peer fixture: %w", startErr)
	}
	stack.containers["fixture"] = fixture
	stack.FixtureControlURL, err = endpoint(ctx, fixture, "8081/tcp", "http")
	if err != nil {
		return nil, err
	}

	toxi, startErr := startContainer(ctx, stack.network, "toxiproxy", testcontainers.ContainerRequest{
		Image: toxiproxyImage, ExposedPorts: []string{"8474/tcp", "8666/tcp"}, Labels: labels,
		WaitingFor: wait.ForListeningPort("8474/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start Toxiproxy: %w", startErr)
	}
	stack.containers["toxiproxy"] = toxi
	stack.ToxiproxyURL, err = endpoint(ctx, toxi, "8474/tcp", "http")
	if err != nil {
		return nil, err
	}
	stack.Toxiproxy, err = toxiclient.NewClient(stack.ToxiproxyURL)
	if err != nil {
		return nil, err
	}
	if err := stack.Toxiproxy.CreateProxy(ctx, proxyName, "0.0.0.0:8666", "fixture:8081"); err != nil {
		return nil, fmt.Errorf("create DSS proxy: %w", err)
	}

	api, startErr := startContainer(ctx, stack.network, "aero-arc-api", testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: config.APISource, Dockerfile: "Dockerfile", Repo: "aero-arc-api", Tag: "e2e", KeepImage: true,
		},
		Env:          apiEnvironment("demo"),
		Files:        apiFiles(config.RootDir),
		ExposedPorts: []string{"8080/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/readyz").WithPort("8080/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start Aero Arc API: %w", startErr)
	}
	stack.containers["aero-arc-api"] = api
	stack.APIBaseURL, err = endpoint(ctx, api, "8080/tcp", "http")
	if err != nil {
		return nil, err
	}
	postGISEndpoint, err := endpoint(ctx, postGIS, "5432/tcp", "")
	if err != nil {
		return nil, err
	}
	stack.DatabaseURL = fmt.Sprintf("postgres://aero_arc:aero_arc_test@%s/aero_arc?sslmode=disable", postGISEndpoint)
	if err := stack.writeManifest(); err != nil {
		return nil, err
	}
	return stack, nil
}

// StopAPI stops the real API process without removing its container or any
// durable state. A zero grace period models abrupt worker loss.
func (stack *Stack) StopAPI(ctx context.Context) error {
	api := stack.containers["aero-arc-api"]
	if api == nil {
		return fmt.Errorf("Aero Arc API container is not available")
	}
	grace := time.Duration(0)
	if err := api.Stop(ctx, &grace); err != nil {
		return fmt.Errorf("stop Aero Arc API: %w", err)
	}
	return nil
}

// StartAPI restarts a previously stopped API container and waits for semantic
// readiness before returning it to the scenario.
func (stack *Stack) StartAPI(ctx context.Context) error {
	api := stack.containers["aero-arc-api"]
	if api == nil {
		return fmt.Errorf("Aero Arc API container is not available")
	}
	if api.IsRunning() {
		return nil
	}
	if err := api.Start(ctx); err != nil {
		return fmt.Errorf("start Aero Arc API: %w", err)
	}
	baseURL, err := endpoint(ctx, api, "8080/tcp", "http")
	if err != nil {
		return err
	}
	stack.APIBaseURL = baseURL
	return stack.waitForAPI(ctx)
}

// StartReplacementAPI starts a new API container against the existing
// PostGIS and DSS state. The stopped container is retained for failure-time log
// capture, while the replacement receives the stable network alias.
func (stack *Stack) StartReplacementAPI(ctx context.Context) error {
	previous := stack.containers["aero-arc-api"]
	if previous == nil {
		return fmt.Errorf("Aero Arc API container is not available")
	}
	if previous.IsRunning() {
		return fmt.Errorf("Aero Arc API must be stopped before replacement")
	}
	labels := map[string]string{"aero-arc.io/test-run": stack.RunID, "aero-arc.io/seed": fmt.Sprint(stack.Seed)}
	replacement, err := startContainer(ctx, stack.network, "aero-arc-api", testcontainers.ContainerRequest{
		Image: "aero-arc-api:e2e", Env: apiEnvironment("none"), Files: apiFiles(stack.rootDir),
		ExposedPorts: []string{"8080/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/readyz").WithPort("8080/tcp").WithStartupTimeout(30 * time.Second),
	})
	if err != nil {
		return fmt.Errorf("start replacement Aero Arc API: %w", err)
	}
	baseURL, err := endpoint(ctx, replacement, "8080/tcp", "http")
	if err != nil {
		_ = testcontainers.TerminateContainer(replacement)
		return err
	}
	stack.containers["aero-arc-api-crashed"] = previous
	stack.containers["aero-arc-api"] = replacement
	stack.APIBaseURL = baseURL
	if err := stack.writeManifest(); err != nil {
		return err
	}
	return nil
}

func (stack *Stack) waitForAPI(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, stack.APIBaseURL+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("create API readiness request: %w", err)
	}
	delay := 50 * time.Millisecond
	for {
		response, requestErr := http.DefaultClient.Do(request.Clone(ctx))
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for restarted Aero Arc API readiness: %w", ctx.Err())
		case <-timer.C:
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}
}

func apiEnvironment(seed string) map[string]string {
	return map[string]string{
		"AERO_API_ADDR": ":8080", "AERO_API_DURABLE_STORE": "postgres",
		"AERO_API_DATABASE_URL":       "postgres://aero_arc:aero_arc_test@postgis:5432/aero_arc?sslmode=disable",
		"AERO_API_AIRSPACE_PROVIDERS": "local,interuss",
		"AERO_API_DSS_BASE_URL":       "http://toxiproxy:8666", "AERO_API_DSS_STATIC_TOKEN": "e2e-static-token",
		"AERO_API_DSS_ALLOW_INSECURE_PEER_URLS": "true", "AERO_API_REQUEST_TIMEOUT": "750ms",
		"AERO_API_USS_BASE_URL":            "http://aero-arc-api:8080",
		"AERO_API_USS_JWT_PUBLIC_KEY_FILE": "/uss-auth-public.pem",
		"AERO_API_USS_JWT_ISSUER":          "e2e-fixture", "AERO_API_USS_JWT_AUDIENCE": "aero-arc-api",
		"AERO_API_TELEMETRY_STORE": "memory", "AERO_API_REPLAY_STORE": "memory", "AERO_API_REGISTRY_MODE": "memory",
		"AERO_API_SEED": seed,
	}
}

func apiFiles(rootDir string) []testcontainers.ContainerFile {
	return []testcontainers.ContainerFile{{
		HostFilePath:      filepath.Join(rootDir, "testdata", "keys", "uss-auth-public.pem"),
		ContainerFilePath: "/uss-auth-public.pem", FileMode: 0o444,
	}}
}

func startContainer(ctx context.Context, testNetwork *testcontainers.DockerNetwork, alias string, request testcontainers.ContainerRequest) (testcontainers.Container, error) {
	request.Networks = []string{testNetwork.Name}
	request.NetworkAliases = map[string][]string{testNetwork.Name: {alias}}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
}

func endpoint(ctx context.Context, container testcontainers.Container, port, scheme string) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve container host: %w", err)
	}
	mapped, err := container.MappedPort(ctx, nat.Port(port))
	if err != nil {
		return "", fmt.Errorf("resolve mapped port %s: %w", port, err)
	}
	value := fmt.Sprintf("%s:%s", host, mapped.Port())
	if scheme != "" {
		value = scheme + "://" + value
	}
	return value, nil
}

// Capture persists logs for all services before teardown.
func (stack *Stack) Capture(ctx context.Context) error {
	logsDir := filepath.Join(stack.ArtifactDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(stack.containers))
	for name := range stack.containers {
		names = append(names, name)
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		reader, err := stack.containers[name].Logs(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		file, err := os.Create(filepath.Join(logsDir, name+".log"))
		if err == nil {
			_, err = io.Copy(file, reader)
			_ = file.Close()
		}
		_ = reader.Close()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close removes containers in reverse dependency order and then the network.
func (stack *Stack) Close(ctx context.Context) error {
	var firstErr error
	for _, name := range []string{"aero-arc-api", "aero-arc-api-crashed", "toxiproxy", "fixture", "postgis"} {
		container := stack.containers[name]
		if container == nil {
			continue
		}
		if err := testcontainers.TerminateContainer(container); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("terminate %s: %w", name, err)
		}
	}
	if stack.network != nil {
		if err := stack.network.Remove(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove network: %w", err)
		}
	}
	return firstErr
}

func (stack *Stack) writeManifest() error {
	manifest := map[string]any{
		"run_id": stack.RunID, "seed": stack.Seed, "created_at": time.Now().UTC(),
		"images":    map[string]string{"postgis": postGISImage, "toxiproxy": toxiproxyImage},
		"endpoints": map[string]string{"api": stack.APIBaseURL, "fixture_control": stack.FixtureControlURL, "toxiproxy": stack.ToxiproxyURL},
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stack.ArtifactDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stack.ArtifactDir, "stack.json"), append(encoded, '\n'), 0o644)
}
