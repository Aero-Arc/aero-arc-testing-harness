// Package realdss provisions the protocol-fidelity federation profile.
package realdss

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Aero-Arc/aero-arc-test-harness/assertions"
)

const (
	postGISImage    = "postgis/postgis:14-3.5-alpine"
	cockroachImage  = "cockroachdb/cockroach:v24.1.3"
	dssImage        = "interuss-local/dss:real-e2e"
	oauthImage      = "interuss-local/dummy-oauth:real-e2e"
	aeroArcAPIImage = "aero-arc-api:real-e2e"
)

type Config struct {
	RootDir           string
	APISource         string
	InterUSSSource    string
	APISourceRevision string
	DSSSourceRevision string
	ArtifactDir       string
	Seed              int64
	StartupLimit      time.Duration
}

type Participant struct {
	Name        string `json:"name"`
	Subject     string `json:"subject"`
	APIBaseURL  string `json:"api_base_url"`
	DatabaseURL string `json:"database_url"`
}

type Environment struct {
	RunID       string
	Seed        int64
	ArtifactDir string
	DSSBaseURL  string
	OAuthURL    string
	USSA        Participant
	USSB        Participant

	network    *testcontainers.DockerNetwork
	containers map[string]testcontainers.Container
}

func Start(ctx context.Context, config Config) (_ *Environment, err error) {
	if config.StartupLimit <= 0 {
		config.StartupLimit = 12 * time.Minute
	}
	if config.RootDir == "" || config.APISource == "" || config.InterUSSSource == "" || config.ArtifactDir == "" {
		return nil, fmt.Errorf("root, API source, InterUSS source, and artifact directories are required")
	}
	for _, required := range []string{
		filepath.Join(config.APISource, "Dockerfile"),
		filepath.Join(config.APISource, "go.mod"),
		filepath.Join(config.InterUSSSource, "Dockerfile"),
		filepath.Join(config.InterUSSSource, "cmds", "dummy-oauth", "Dockerfile"),
		filepath.Join(config.InterUSSSource, "build", "test-certs", "auth2.pem"),
	} {
		if _, statErr := os.Stat(required); statErr != nil {
			return nil, fmt.Errorf("required real-DSS input %s: %w", required, statErr)
		}
	}

	runID := fmt.Sprintf("real-dss-%d-%d", time.Now().UTC().Unix(), config.Seed)
	environment := &Environment{
		RunID: runID, Seed: config.Seed, ArtifactDir: config.ArtifactDir,
		containers: make(map[string]testcontainers.Container),
	}
	defer func() {
		if err != nil {
			_ = environment.Capture(context.Background())
			_ = environment.Close(context.Background())
		}
	}()

	environment.network, err = network.New(ctx, network.WithLabels(map[string]string{"aero-arc.io/test-run": runID}))
	if err != nil {
		return nil, fmt.Errorf("create real-DSS network: %w", err)
	}
	labels := map[string]string{
		"aero-arc.io/test-run": runID,
		"aero-arc.io/seed":     fmt.Sprint(config.Seed),
		"aero-arc.io/tier":     "real-dss",
	}

	crdb, startErr := startContainer(ctx, environment.network, "interuss-crdb", testcontainers.ContainerRequest{
		Image: cockroachImage,
		Cmd: []string{
			"start-single-node", "--insecure", "--listen-addr=:26257", "--http-addr=:8080",
		},
		ExposedPorts: []string{"8080/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/health?ready=1").WithPort("8080/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start InterUSS CockroachDB: %w", startErr)
	}
	environment.containers["interuss-crdb"] = crdb

	migration, startErr := startContainer(ctx, environment.network, "interuss-scd-migration", testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: config.InterUSSSource, Dockerfile: "Dockerfile", Repo: "interuss-local/dss", Tag: "real-e2e", KeepImage: true,
		},
		Cmd: []string{
			"/usr/bin/db-manager", "migrate", "--schemas_dir=/db-schemas/scd", "--db_version=latest", "--datastore_host=interuss-crdb",
		},
		Labels: labels, WaitingFor: wait.ForExit().WithExitTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("run InterUSS SCD migration: %w", startErr)
	}
	environment.containers["interuss-scd-migration"] = migration
	if err := requireSuccessfulExit(ctx, migration, "InterUSS SCD migration"); err != nil {
		return nil, err
	}
	for _, schema := range []string{"rid", "aux_"} {
		name := "interuss-" + schema + "-migration"
		if schema == "aux_" {
			name = "interuss-aux-migration"
		}
		migration, startErr = startContainer(ctx, environment.network, name, testcontainers.ContainerRequest{
			Image: dssImage,
			Cmd: []string{
				"/usr/bin/db-manager", "migrate", "--schemas_dir=/db-schemas/" + schema,
				"--db_version=latest", "--datastore_host=interuss-crdb",
			},
			Labels: labels, WaitingFor: wait.ForExit().WithExitTimeout(config.StartupLimit),
		})
		if startErr != nil {
			return nil, fmt.Errorf("run InterUSS %s migration: %w", schema, startErr)
		}
		environment.containers[name] = migration
		if err := requireSuccessfulExit(ctx, migration, "InterUSS "+schema+" migration"); err != nil {
			return nil, err
		}
	}

	oauth, startErr := startContainer(ctx, environment.network, "interuss-oauth", testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: config.InterUSSSource, Dockerfile: "cmds/dummy-oauth/Dockerfile", Repo: "interuss-local/dummy-oauth", Tag: "real-e2e", KeepImage: true,
		},
		Cmd:          []string{"-private_key_file", "/var/test-certs/auth2.key"},
		ExposedPorts: []string{"8085/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/token?grant_type=client_credentials&scope=utm.strategic_coordination&intended_audience=localhost&issuer=localhost&sub=harness-readiness").
			WithPort("8085/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start InterUSS dummy OAuth: %w", startErr)
	}
	environment.containers["interuss-oauth"] = oauth
	environment.OAuthURL, err = endpoint(ctx, oauth, "8085/tcp", "http")
	if err != nil {
		return nil, err
	}
	environment.OAuthURL += "/token"

	core, startErr := startContainer(ctx, environment.network, "interuss-dss", testcontainers.ContainerRequest{
		Image: dssImage,
		Cmd: []string{
			"/usr/bin/core-service",
			"-datastore_host", "interuss-crdb",
			"-public_key_files", "/test-certs/auth2.pem",
			"-log_format", "console",
			"-dump_requests",
			"-addr", ":8082",
			"-accepted_jwt_audiences", "localhost",
			"-enable_scd",
			"-allow_http_base_urls",
			"-locality", "aero_arc_e2e",
			"-public_endpoint", "http://interuss-dss:8082",
		},
		ExposedPorts: []string{"8082/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/healthy").WithPort("8082/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start InterUSS core service: %w", startErr)
	}
	environment.containers["interuss-dss"] = core
	environment.DSSBaseURL, err = endpoint(ctx, core, "8082/tcp", "http")
	if err != nil {
		return nil, err
	}

	postGISA, err := environment.startPostGIS(ctx, "postgis-a", labels, config.StartupLimit)
	if err != nil {
		return nil, err
	}
	postGISB, err := environment.startPostGIS(ctx, "postgis-b", labels, config.StartupLimit)
	if err != nil {
		return nil, err
	}

	apiA, startErr := startContainer(ctx, environment.network, "uss-a", testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: config.APISource, Dockerfile: "Dockerfile", Repo: "aero-arc-api", Tag: "real-e2e", KeepImage: true,
		},
		Env: apiEnvironment("a", "uss-a", "postgis-a"),
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      filepath.Join(config.InterUSSSource, "build", "test-certs", "auth2.pem"),
			ContainerFilePath: "/interuss-auth-public.pem", FileMode: 0o444,
		}},
		ExposedPorts: []string{"8080/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/readyz").WithPort("8080/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start Aero Arc USS-A: %w", startErr)
	}
	environment.containers["uss-a"] = apiA

	apiB, startErr := startContainer(ctx, environment.network, "uss-b", testcontainers.ContainerRequest{
		Image: aeroArcAPIImage,
		Env:   apiEnvironment("b", "uss-b", "postgis-b"),
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      filepath.Join(config.InterUSSSource, "build", "test-certs", "auth2.pem"),
			ContainerFilePath: "/interuss-auth-public.pem", FileMode: 0o444,
		}},
		ExposedPorts: []string{"8080/tcp"}, Labels: labels,
		WaitingFor: wait.ForHTTP("/readyz").WithPort("8080/tcp").WithStartupTimeout(config.StartupLimit),
	})
	if startErr != nil {
		return nil, fmt.Errorf("start Aero Arc USS-B: %w", startErr)
	}
	environment.containers["uss-b"] = apiB

	environment.USSA, err = participant(ctx, "uss-a", "uss-a", apiA, postGISA)
	if err != nil {
		return nil, err
	}
	environment.USSB, err = participant(ctx, "uss-b", "uss-b", apiB, postGISB)
	if err != nil {
		return nil, err
	}
	if err := environment.writeManifest(config); err != nil {
		return nil, err
	}
	return environment, nil
}

func (environment *Environment) startPostGIS(ctx context.Context, alias string, labels map[string]string, startupLimit time.Duration) (testcontainers.Container, error) {
	container, err := startContainer(ctx, environment.network, alias, testcontainers.ContainerRequest{
		Image: postGISImage,
		Env: map[string]string{
			"POSTGRES_DB": "aero_arc", "POSTGRES_USER": "aero_arc", "POSTGRES_PASSWORD": "aero_arc_test",
		},
		ExposedPorts: []string{"5432/tcp"}, Labels: labels,
		WaitingFor: wait.ForAll(
			wait.ForLog("PostgreSQL init process complete; ready for start up."),
			wait.ForExec([]string{"pg_isready", "-U", "aero_arc", "-d", "aero_arc"}),
		).WithDeadline(startupLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", alias, err)
	}
	environment.containers[alias] = container
	return container, nil
}

func apiEnvironment(suffix, alias, databaseAlias string) map[string]string {
	return map[string]string{
		"AERO_API_ADDR":                         ":8080",
		"AERO_API_DURABLE_STORE":                "postgres",
		"AERO_API_DATABASE_URL":                 fmt.Sprintf("postgres://aero_arc:aero_arc_test@%s:5432/aero_arc?sslmode=disable", databaseAlias),
		"AERO_API_AIRSPACE_PROVIDERS":           "local,interuss",
		"AERO_API_DSS_BASE_URL":                 "http://interuss-dss:8082",
		"AERO_API_DSS_OAUTH_TOKEN_URL":          "http://interuss-oauth:8085/token",
		"AERO_API_DSS_OAUTH_AUDIENCE":           "localhost",
		"AERO_API_DSS_OAUTH_ISSUER":             "localhost",
		"AERO_API_DSS_OAUTH_SUBJECT":            "uss-" + suffix,
		"AERO_API_DSS_ALLOW_INSECURE_PEER_URLS": "true",
		"AERO_API_USS_BASE_URL":                 "http://" + alias + ":8080",
		"AERO_API_USS_JWT_PUBLIC_KEY_FILE":      "/interuss-auth-public.pem",
		"AERO_API_USS_JWT_ISSUER":               "localhost",
		"AERO_API_USS_JWT_AUDIENCE":             "localhost",
		"AERO_API_REQUEST_TIMEOUT":              "3s",
		"AERO_API_TELEMETRY_STORE":              "memory",
		"AERO_API_REPLAY_STORE":                 "memory",
		"AERO_API_REGISTRY_MODE":                "memory",
		"AERO_API_SEED":                         "demo",
	}
}

func participant(ctx context.Context, name, subject string, api, database testcontainers.Container) (Participant, error) {
	apiURL, err := endpoint(ctx, api, "8080/tcp", "http")
	if err != nil {
		return Participant{}, err
	}
	databaseEndpoint, err := endpoint(ctx, database, "5432/tcp", "")
	if err != nil {
		return Participant{}, err
	}
	return Participant{
		Name: name, Subject: subject, APIBaseURL: apiURL,
		DatabaseURL: fmt.Sprintf("postgres://aero_arc:aero_arc_test@%s/aero_arc?sslmode=disable", databaseEndpoint),
	}, nil
}

// Observer returns a direct, independently authenticated real-DSS observer for
// one USS identity. Using the owner identity ensures the DSS returns its OVN.
func (environment *Environment) Observer(subject string) (*ReferenceObserver, error) {
	if subject == "" {
		return nil, fmt.Errorf("real-DSS observer subject is required")
	}
	return &ReferenceObserver{
		dssBaseURL: environment.DSSBaseURL, tokenURL: environment.OAuthURL,
		subject: subject, http: &http.Client{Timeout: 5 * time.Second},
	}, nil
}

type ReferenceObserver struct {
	dssBaseURL string
	tokenURL   string
	subject    string
	http       *http.Client
}

func (observer *ReferenceObserver) ObserveDSSReference(ctx context.Context, intentID string) (assertions.DSSReference, error) {
	token, err := observer.token(ctx)
	if err != nil {
		return assertions.DSSReference{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, observer.dssBaseURL+"/dss/v1/operational_intent_references/"+url.PathEscape(intentID), nil)
	if err != nil {
		return assertions.DSSReference{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := observer.http.Do(request)
	if err != nil {
		return assertions.DSSReference{}, fmt.Errorf("get InterUSS operational intent reference: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return assertions.DSSReference{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return assertions.DSSReference{}, err
	}
	if response.StatusCode != http.StatusOK {
		return assertions.DSSReference{}, fmt.Errorf("get InterUSS operational intent reference returned %s: %s", response.Status, body)
	}
	var decoded struct {
		Reference struct {
			Version    int    `json:"version"`
			State      string `json:"state"`
			OVN        string `json:"ovn"`
			Manager    string `json:"manager"`
			USSBaseURL string `json:"uss_base_url"`
		} `json:"operational_intent_reference"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return assertions.DSSReference{}, fmt.Errorf("decode InterUSS operational intent reference: %w", err)
	}
	reference := decoded.Reference
	return assertions.DSSReference{
		Exists: true, Version: reference.Version, State: reference.State, OVN: reference.OVN,
		Manager: reference.Manager, USSBaseURL: reference.USSBaseURL,
	}, nil
}

func (observer *ReferenceObserver) token(ctx context.Context) (string, error) {
	tokenURL, err := url.Parse(observer.tokenURL)
	if err != nil {
		return "", err
	}
	query := tokenURL.Query()
	query.Set("grant_type", "client_credentials")
	query.Set("scope", "utm.strategic_coordination")
	query.Set("intended_audience", "localhost")
	query.Set("issuer", "localhost")
	query.Set("sub", observer.subject)
	tokenURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := observer.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("get InterUSS observer token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get InterUSS observer token returned %s", response.Status)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode InterUSS observer token: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("InterUSS observer token response was empty")
	}
	return body.AccessToken, nil
}

func startContainer(ctx context.Context, testNetwork *testcontainers.DockerNetwork, alias string, request testcontainers.ContainerRequest) (testcontainers.Container, error) {
	request.Networks = []string{testNetwork.Name}
	request.NetworkAliases = map[string][]string{testNetwork.Name: {alias}}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
}

func requireSuccessfulExit(ctx context.Context, container testcontainers.Container, name string) error {
	state, err := container.State(ctx)
	if err != nil {
		return fmt.Errorf("read %s state: %w", name, err)
	}
	if state.ExitCode != 0 {
		return fmt.Errorf("%s exited with code %d", name, state.ExitCode)
	}
	return nil
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

func (environment *Environment) Capture(ctx context.Context) error {
	logsDir := filepath.Join(environment.ArtifactDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(environment.containers))
	for name := range environment.containers {
		names = append(names, name)
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		reader, err := environment.containers[name].Logs(ctx)
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

func (environment *Environment) Close(ctx context.Context) error {
	var firstErr error
	for _, name := range []string{
		"uss-b", "uss-a", "postgis-b", "postgis-a", "interuss-dss",
		"interuss-oauth", "interuss-aux-migration", "interuss-rid-migration",
		"interuss-scd-migration", "interuss-crdb",
	} {
		container := environment.containers[name]
		if container == nil {
			continue
		}
		if err := testcontainers.TerminateContainer(container); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("terminate %s: %w", name, err)
		}
	}
	if environment.network != nil {
		if err := environment.network.Remove(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove real-DSS network: %w", err)
		}
	}
	return firstErr
}

func (environment *Environment) writeManifest(config Config) error {
	manifest := map[string]any{
		"run_id": environment.RunID, "seed": environment.Seed, "tier": "real-dss-two-uss", "created_at": time.Now().UTC(),
		"images": map[string]string{
			"postgis": postGISImage, "cockroachdb": cockroachImage, "interuss_dss": dssImage,
			"interuss_oauth": oauthImage, "aero_arc_api": aeroArcAPIImage,
		},
		"source_revisions": map[string]string{"aero_arc_api": config.APISourceRevision, "interuss_dss": config.DSSSourceRevision},
		"endpoints":        map[string]string{"dss": environment.DSSBaseURL, "oauth": environment.OAuthURL},
		"participants":     []Participant{environment.USSA, environment.USSB},
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(environment.ArtifactDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(environment.ArtifactDir, "stack.json"), append(encoded, '\n'), 0o644)
}
