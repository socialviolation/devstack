package svcconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const externalMarker = "<external:secretRef>"

const maskedValue = "••••"

const MaskedValue = maskedValue

type ConfigEntry struct {
	Key        string
	Value      string
	Source     string
	Overridden bool
	Secret     bool
}

// EffectiveConfig computes what a service in a stack WOULD run with. It reads the
// service's declared config sources in order (later wins), applies the stack's
// port overlay on top (highest precedence), and masks secret-looking values. It
// reads only — it never writes or applies anything.
func EffectiveConfig(svc config.ResolvedService, stackName string) ([]ConfigEntry, error) {
	if svc.Manifest == nil {
		return nil, fmt.Errorf("service %q has no manifest", svc.Name)
	}
	cfg := svc.Manifest.Config

	values := map[string]string{}
	provenance := map[string]string{}
	for _, rel := range cfg.Sources {
		src, err := readSource(svc.RepoPath, rel)
		if err != nil {
			return nil, err
		}
		label := sourceLabel(rel)
		for k, v := range src {
			values[k] = v
			provenance[k] = label
		}
	}

	entries := map[string]ConfigEntry{}
	for k, v := range values {
		val := v
		secret := IsSecret(k, v)
		switch {
		case secret:
			val = maskedValue
		case credentialRe.MatchString(v):
			val = credentialRe.ReplaceAllString(v, "$1="+maskedValue)
			secret = true
		}
		entries[k] = ConfigEntry{Key: k, Value: val, Source: provenance[k], Secret: secret}
	}

	if cfg.PortEnv != "" {
		allocated, err := workspace.LoadPorts(stackName)
		if err != nil {
			return nil, err
		}
		if port, ok := allocated[stack.QualifyPortKey(svc.Name, "http")]; ok {
			entries[cfg.PortEnv] = ConfigEntry{
				Key:        cfg.PortEnv,
				Value:      renderPort(cfg.PortEnv, port),
				Source:     "stack",
				Overridden: true,
			}
		}
	}

	out := make([]ConfigEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// readSource reads one declared config source into a flat env-key map. The type
// is detected by extension: *.json is a dotnet appsettings file (nested objects
// flattened with __), *.yml/*.yaml is a k8s Deployment (the container env list).
// A missing or unrecognized source is a hard error naming the file.
func readSource(repoRoot, rel string) (map[string]string, error) {
	path := filepath.Join(repoRoot, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config source %s: %w", rel, err)
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".json":
		return readAppsettings(rel, data)
	case ".yml", ".yaml":
		return readDeployment(rel, data)
	default:
		return nil, fmt.Errorf("config source %s: unrecognized type (want a .json appsettings file or a .yml/.yaml k8s Deployment)", rel)
	}
}

func readAppsettings(rel string, data []byte) (map[string]string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("config source %s: parse json: %w", rel, err)
	}
	out := map[string]string{}
	flatten("", root, out)
	return out, nil
}

func flatten(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			flatten(join(prefix, k), val, out)
		}
	case []any:
		for i, val := range t {
			flatten(join(prefix, strconv.Itoa(i)), val, out)
		}
	case nil:
		out[prefix] = ""
	default:
		out[prefix] = fmt.Sprint(t)
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "__" + key
}

type k8sEnv struct {
	Name      string     `yaml:"name"`
	Value     string     `yaml:"value"`
	ValueFrom *yaml.Node `yaml:"valueFrom"`
}

type k8sDoc struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Env []k8sEnv `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// readDeployment extracts the config surface from the first Deployment document's
// first container: every `- name: X` env entry contributes key X. A literal
// `value:` is taken as-is; a `valueFrom:` (secretRef/configMapRef) records the
// key with the external marker, since the value is supplied elsewhere. The first
// container is the app container; sidecars are not the service's own surface.
func readDeployment(rel string, data []byte) (map[string]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc k8sDoc
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("config source %s: parse yaml: %w", rel, err)
		}
		if doc.Kind != "Deployment" {
			continue
		}
		containers := doc.Spec.Template.Spec.Containers
		if len(containers) == 0 {
			return nil, fmt.Errorf("config source %s: Deployment has no containers", rel)
		}
		out := map[string]string{}
		for _, e := range containers[0].Env {
			if e.ValueFrom != nil {
				out[e.Name] = externalMarker
				continue
			}
			out[e.Name] = e.Value
		}
		return out, nil
	}
	return nil, fmt.Errorf("config source %s: no Deployment document found", rel)
}

func sourceLabel(rel string) string {
	if strings.EqualFold(filepath.Ext(rel), ".json") {
		return "appsettings"
	}
	return "k8s"
}

func renderPort(key string, port int) string {
	if strings.Contains(strings.ToUpper(key), "URL") {
		return fmt.Sprintf("http://localhost:%d", port)
	}
	return strconv.Itoa(port)
}

var secretSubstrings = []string{"connectionstring", "secret", "token", "password", "key"}

// credentialRe matches a secret smuggled into a value as `param=<secret>` — an
// Azure function `?code=`, a Redis/SQL `password=`, a SAS `sig=`, etc. — so the
// value is redacted in place while the surrounding URL/string stays legible.
// Key-name masking (IsSecret) misses these because the key looks innocent.
var credentialRe = regexp.MustCompile(`(?i)(code|sig|passwo?rd|accountkey|sharedaccesskey|secret|token|api[_-]?key)=[^&;\s"']+`)

func IsSecret(key, value string) bool {
	if value == externalMarker {
		return true
	}
	lower := strings.ToLower(key)
	for _, s := range secretSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
