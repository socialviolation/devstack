// Package openobserve provides the default OTEL plugin for devstack: a single
// OpenObserve container per machine, shared by every workspace. Telemetry is
// sliced per workspace and stack at query time using the devstack.* resource
// attributes services are stamped with, not by running a stack each.
package openobserve

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/observability"
	obsopenobserve "github.com/socialviolation/devstack/internal/observability/openobserve"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/workspace"
)

func init() {
	otel.Register(&Plugin{})
}

const (
	// ContainerName is the one OpenObserve container devstack manages per machine.
	ContainerName = "devstack-observability"
	volumeName    = "devstack-observability-data"
	image         = "openobserve/openobserve:v0.91.3"

	// UIPort serves the web UI, the OTLP/HTTP ingest API, and the query API.
	UIPort = 5080
	// GRPCPort serves OTLP/gRPC ingest.
	GRPCPort = 5081

	// Org is the OpenObserve organisation devstack ingests into and queries.
	Org = "default"
	// Stream is the stream name for each signal type.
	Stream = "default"
)

// Plugin runs OpenObserve locally and points the collector at it.
type Plugin struct{}

func (p *Plugin) Name() string { return "openobserve" }

// Contribute exports this workspace's telemetry to the local OpenObserve over
// OTLP/HTTP. Resource attributes are left untouched — services already carry
// devstack.workspace, devstack.stack and devstack.service.
func (p *Plugin) Contribute(ws *workspace.Workspace) (otel.Contribution, error) {
	creds, err := Credentials()
	if err != nil {
		return otel.Contribution{}, err
	}

	exporter := map[string]any{
		"endpoint": fmt.Sprintf("http://localhost:%d/api/%s", UIPort, Org),
		"headers": map[string]any{
			"Authorization": "Basic " + creds.Token(),
			"stream-name":   Stream,
		},
	}
	pipeline := otel.Pipeline{
		Processors: []string{"batch"},
		Exporters:  []string{"otlphttp/openobserve"},
	}

	return otel.Contribution{
		Processors: map[string]any{
			"batch": map[string]any{"timeout": "2s", "send_batch_size": 8192},
		},
		Exporters: map[string]any{"otlphttp/openobserve": exporter},
		Traces:    pipeline,
		Metrics:   pipeline,
		Logs:      pipeline,
	}, nil
}

// StartCompanion ensures the one OpenObserve container is up, serving, and
// running the image this build pins. A container created by an older devstack
// keeps its old image forever otherwise, so an upgrade would silently do
// nothing — the data volume is separate, so replacing the container is safe.
func (p *Plugin) StartCompanion(ws *workspace.Workspace) error {
	stale := p.CompanionStale(ws)

	if p.CompanionRunning(ws) && !stale {
		return awaitReady()
	}

	creds, err := Credentials()
	if err != nil {
		return err
	}

	if stale {
		fmt.Fprintf(os.Stderr, "Upgrading OpenObserve from %s to %s (data is kept)...\n", containerImage(), image)
		if out, err := exec.Command("docker", "rm", "-f", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to replace %s: %s", ContainerName, strings.TrimSpace(string(out)))
		}
	} else if containerExists() {
		if out, err := exec.Command("docker", "start", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start %s: %s", ContainerName, strings.TrimSpace(string(out)))
		}
		return awaitReady()
	}

	args := []string{
		"run", "-d",
		"--name", ContainerName,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%d:5080", UIPort),
		"-p", fmt.Sprintf("%d:5081", GRPCPort),
		"-v", volumeName + ":/data",
		"-e", "ZO_DATA_DIR=/data",
		"-e", "ZO_ROOT_USER_EMAIL=" + creds.Email,
		"-e", "ZO_ROOT_USER_PASSWORD=" + creds.Password,
		"-e", "ZO_TELEMETRY=false",
		image,
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start OpenObserve: %s", strings.TrimSpace(string(out)))
	}
	return awaitReady()
}

// StopCompanion stops the container but keeps its data volume.
func (p *Plugin) StopCompanion(ws *workspace.Workspace) error {
	if !containerExists() {
		return nil
	}
	if out, err := exec.Command("docker", "stop", ContainerName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop OpenObserve: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CompanionStale reports whether the existing container was created from an
// image other than the one this build pins.
func (p *Plugin) CompanionStale(ws *workspace.Workspace) bool {
	return containerExists() && containerImage() != image
}

func (p *Plugin) CompanionRunning(ws *workspace.Workspace) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", ContainerName).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// QueryEndpoint returns the OpenObserve UI, which is also the query API base.
func (p *Plugin) QueryEndpoint(ws *workspace.Workspace) string {
	return fmt.Sprintf("http://localhost:%d", UIPort)
}

// Backend returns a query client for the local OpenObserve.
func (p *Plugin) Backend(ws *workspace.Workspace) (observability.Backend, error) {
	creds, err := Credentials()
	if err != nil {
		return nil, err
	}
	return obsopenobserve.NewClient(p.QueryEndpoint(ws), creds.Token()), nil
}

func (p *Plugin) Validate(ws *workspace.Workspace) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH — required for the openobserve plugin")
	}
	return nil
}

func (p *Plugin) ConfigSchema() []otel.ConfigField { return nil }

// --- credentials ---

// Creds are the generated root credentials for the local OpenObserve. They are
// machine-local and never written to a committed file.
type Creds struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Token returns the base64 basic-auth token OpenObserve's API expects.
func (c Creds) Token() string {
	return base64.StdEncoding.EncodeToString([]byte(c.Email + ":" + c.Password))
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "openobserve", "credentials.json"), nil
}

// Credentials returns the local OpenObserve root credentials, generating and
// persisting them on first use. They are only ever bound to localhost.
func Credentials() (Creds, error) {
	path, err := credentialsPath()
	if err != nil {
		return Creds{}, err
	}

	if data, err := os.ReadFile(path); err == nil {
		var c Creds
		if err := json.Unmarshal(data, &c); err == nil && c.Email != "" && c.Password != "" {
			return c, nil
		}
	}

	pw, err := generatePassword()
	if err != nil {
		return Creds{}, err
	}
	c := Creds{Email: "devstack@localhost.local", Password: pw}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return Creds{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Creds{}, fmt.Errorf("failed to create credentials dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return Creds{}, fmt.Errorf("failed to write credentials: %w", err)
	}
	return c, nil
}

// generatePassword builds a password OpenObserve's policy accepts: at least one
// lower, upper, digit and special character. A container that gets a weaker one
// panics on boot rather than rejecting it.
func generatePassword() (string, error) {
	const (
		lower   = "abcdefghijkmnopqrstuvwxyz"
		upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		digits  = "23456789"
		special = "!@#$%^&*-_"
	)
	classes := []string{lower, upper, digits, special}
	all := lower + upper + digits + special

	out := make([]byte, 24)
	for i := range out {
		set := all
		if i < len(classes) {
			set = classes[i]
		}
		c, err := randomByte(set)
		if err != nil {
			return "", err
		}
		out[i] = c
	}
	return shuffle(out)
}

func randomByte(set string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, fmt.Errorf("failed to generate credentials: %w", err)
	}
	return set[n.Int64()], nil
}

func shuffle(b []byte) (string, error) {
	for i := len(b) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", fmt.Errorf("failed to generate credentials: %w", err)
		}
		j := n.Int64()
		b[i], b[j] = b[j], b[i]
	}
	return string(b), nil
}

func awaitReady() error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/healthz", UIPort)
	var lastErr error
	for i := 0; i < 60; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("healthz returned %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("OpenObserve did not become ready on :%d — %w (check: docker logs %s)", UIPort, lastErr, ContainerName)
}

func containerExists() bool {
	err := exec.Command("docker", "inspect", ContainerName).Run()
	return err == nil
}

// containerImage returns the image the existing container was created from, or
// "" when there is no container.
func containerImage() string {
	out, err := exec.Command("docker", "inspect", "-f", "{{.Config.Image}}", ContainerName).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
