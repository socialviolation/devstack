package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// editWorkspaceManifest applies mutate to the manifest's root mapping node and
// writes the result back, preserving comments and unrelated fields. It operates
// on the raw YAML node tree rather than the typed struct so hand-authored
// formatting survives the round-trip.
func editWorkspaceManifest(workspacePath string, mutate func(root *yaml.Node) error) error {
	if !HasWorkspaceManifest(workspacePath) {
		return fmt.Errorf("there is no %s in %s. The configuration lives in the workspace manifest", WorkspaceManifestFileName, workspacePath)
	}
	return editManifest(WorkspaceManifestPath(workspacePath), mutate)
}

// editManifest applies mutate to the manifest's root mapping node at path and
// writes the result back.
//
// A file can hold more than one YAML document. devstack reads only the first
// one, but it writes back every one of them: the others are the user's, and a
// migration that sweeps every workspace must not delete what it does not read.
func editManifest(path string, mutate func(root *yaml.Node) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("can not read the manifest %s: %w", path, err)
	}

	docs, err := decodeDocuments(data)
	if err != nil {
		return fmt.Errorf("can not parse the manifest %s: %w", path, err)
	}
	if len(docs) == 0 || len(docs[0].Content) == 0 || docs[0].Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("the manifest %s is not a mapping", path)
	}

	if err := mutate(docs[0].Content[0]); err != nil {
		return err
	}

	var buf bytes.Buffer
	if startsWithDocumentMarker(data) {
		buf.WriteString("---\n")
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("can not encode the manifest %s: %w", path, err)
		}
	}
	enc.Close()

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("can not write the manifest %s: %w", path, err)
	}
	return nil
}

// decodeDocuments parses every YAML document in data, in file order.
func decodeDocuments(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, &doc)
	}
	return docs, nil
}

// startsWithDocumentMarker reports whether the file opens with an explicit "---"
// line. The encoder writes a marker between documents, but not before the first
// one, so a file that had one keeps it here.
func startsWithDocumentMarker(data []byte) bool {
	rest := strings.TrimLeft(string(data), " \t\r\n")
	return rest == "---" || strings.HasPrefix(rest, "---\n") || strings.HasPrefix(rest, "--- ") || strings.HasPrefix(rest, "---\r\n")
}

// WorkspaceVersion reports the version of the workspace manifest at
// workspacePath. A directory that holds no workspace manifest has no version,
// and it reports 0.
func WorkspaceVersion(workspacePath string) (int, error) {
	if !HasWorkspaceManifest(workspacePath) {
		return 0, nil
	}
	m, err := LoadWorkspaceManifest(workspacePath)
	if err != nil {
		return 0, err
	}
	return m.Version, nil
}

// SetWorkspaceVersion writes version into the workspace manifest, and a note
// beside it that says which devstack wrote it and when.
//
// The note is for a person who reads the file. devstack never reads it back: the
// version alone decides what a migration runs.
func SetWorkspaceVersion(workspacePath string, version int, by string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		setScalar(root, "version", strconv.Itoa(version), "!!int")
		mapValue(root, "version").LineComment = fmt.Sprintf("devstack migrate wrote this: %s, %s", by, time.Now().Format("2006-01-02"))
		return nil
	})
}

// SetServiceEnvValue writes key=value into the service manifest's env.values at
// repoPath, creating the env.values block if absent. It preserves comments and
// unrelated fields.
//
// env.values is committed to git: callers must not route secrets here.
func SetServiceEnvValue(repoPath, key, value string) error {
	if !HasServiceManifest(repoPath) {
		return fmt.Errorf("there is no %s in %s", ServiceManifestFileName, repoPath)
	}
	return editManifest(ServiceManifestPath(repoPath), func(root *yaml.Node) error {
		values := mappingChild(mappingChild(root, "env"), "values")
		if values.Kind != yaml.MappingNode {
			return fmt.Errorf("env.values in %s is not a mapping", ServiceManifestPath(repoPath))
		}
		setScalar(values, key, value, "!!str")
		return nil
	})
}

// SetEnvValue writes environments.<envName>.values.<key>=value into the
// workspace manifest, creating the environments, env, and values mappings as
// needed. It preserves comments and unrelated fields.
func SetEnvValue(workspacePath, envName, key, value string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		values := mappingChild(mappingChild(mappingChild(root, "environments"), envName), "values")
		if values.Kind != yaml.MappingNode {
			return fmt.Errorf("environments.%s.values is not a mapping", envName)
		}
		setScalar(values, key, value, "!!str")
		return nil
	})
}

// RemoveEnvironment deletes environments.<envName> from the workspace manifest.
func RemoveEnvironment(workspacePath, envName string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		envs := mapValue(root, "environments")
		if envs == nil || envs.Kind != yaml.MappingNode {
			return fmt.Errorf("%s does not define the environment %q", WorkspaceManifestFileName, envName)
		}
		deleteKey(envs, envName)
		return nil
	})
}

// SetWorkspaceEnv sets workspace.env to envName in the workspace manifest,
// selecting the active env at the workspace scope.
func SetWorkspaceEnv(workspacePath, envName string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		setScalar(mappingChild(root, "workspace"), "env", envName, "!!str")
		return nil
	})
}

// SetServiceEnv sets service.env to envName in the service manifest at repoPath,
// selecting the active env at the service scope.
func SetServiceEnv(repoPath, envName string) error {
	if !HasServiceManifest(repoPath) {
		return fmt.Errorf("there is no %s in %s", ServiceManifestFileName, repoPath)
	}
	return editManifest(ServiceManifestPath(repoPath), func(root *yaml.Node) error {
		setScalar(mappingChild(root, "service"), "env", envName, "!!str")
		return nil
	})
}

// SetObservabilityEnabled writes observability.enabled into the workspace
// manifest, creating the observability block if absent.
func SetObservabilityEnabled(workspacePath string, enabled bool) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		obs := mappingChild(root, "observability")
		setScalar(obs, "enabled", boolValue(enabled), "!!bool")
		return nil
	})
}

// SetObservabilityBackend writes observability.backend into the workspace
// manifest, creating the observability block if absent. An empty backend clears
// the key so the default (openobserve) applies.
func SetObservabilityBackend(workspacePath string, backend string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		obs := mappingChild(root, "observability")
		if backend == "" {
			deleteKey(obs, "backend")
			return nil
		}
		setScalar(obs, "backend", backend, "!!str")
		return nil
	})
}

// credentialKeySubstrings mark a config key name as carrying a credential.
var credentialKeySubstrings = []string{"connectionstring", "secret", "token", "password", "key"}

// IsCredentialKey reports whether a config key name reads as a credential and
// so must never be written to a committed file.
func IsCredentialKey(key string) bool {
	lower := strings.ToLower(key)
	// A name that only labels a credential is not itself one: api_key_header
	// carries a header's name, sharedAccessKeyName a policy's name.
	for _, suffix := range []string{"header", "name"} {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	for _, s := range credentialKeySubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// SetObservabilitySettings merges settings into observability.settings in the
// workspace manifest, creating the block if absent. Existing keys not named in
// settings are left alone.
//
// The workspace manifest is committed to git, so a credential-named key is
// refused outright rather than persisted.
func SetObservabilitySettings(workspacePath string, settings map[string]string) error {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		if IsCredentialKey(key) {
			return fmt.Errorf("devstack never writes a credential to %s, so it refuses the key %q. Supply the value through the environment (.envrc), or through env.required of the service", WorkspaceManifestFileName, key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		obs := mappingChild(root, "observability")
		values := mappingChild(obs, "settings")
		if values.Kind != yaml.MappingNode {
			return fmt.Errorf("observability.settings is not a mapping")
		}
		for _, key := range keys {
			setScalar(values, key, settings[key], "!!str")
		}
		return nil
	})
}

// AddServiceRepo registers repoRelPath in the workspace manifest's explicit
// repoDiscovery.repos list. It is idempotent — a no-op if the path is already
// listed — and preserves comments and formatting.
func AddServiceRepo(workspacePath, repoRelPath string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		wsNode := mappingChild(root, "workspace")
		rd := mappingChild(wsNode, "repoDiscovery")
		repos := mapValue(rd, "repos")
		if repos == nil {
			repos = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			rd.Content = append(rd.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "repos"}, repos)
		}
		if repos.Kind != yaml.SequenceNode {
			return fmt.Errorf("workspace.repoDiscovery.repos is not a list")
		}
		for _, item := range repos.Content {
			if item.Value == repoRelPath {
				return nil // already registered
			}
		}
		repos.Content = append(repos.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: repoRelPath})
		return nil
	})
}

// SetGroupMembers replaces a group's membership. An empty members list removes
// the group entirely.
func SetGroupMembers(workspacePath, group string, members []string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		return setStringList(root, "groups", group, members)
	})
}

// SetServiceDependencies replaces a service's dependency list. An empty list
// removes the entry.
func SetServiceDependencies(workspacePath, service string, deps []string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		return setStringList(root, "dependencies", service, deps)
	})
}

// setStringList sets key to a flow-style sequence of values inside root's
// section mapping, reusing the existing sequence's style when the key is already
// present. An empty values list removes the key.
func setStringList(root *yaml.Node, section, key string, values []string) error {
	if len(values) == 0 {
		m := mapValue(root, section)
		if m == nil || m.Kind != yaml.MappingNode {
			return nil
		}
		deleteKey(m, key)
		if len(m.Content) == 0 {
			deleteKey(root, section)
		}
		return nil
	}

	m := mappingChild(root, section)
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a mapping", section)
	}
	seq := mapValue(m, key)
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
	} else if seq.Kind != yaml.SequenceNode {
		if seq.Kind != yaml.ScalarNode || seq.Tag != "!!null" {
			return fmt.Errorf("%s.%s is not a list", section, key)
		}
		seq.Kind = yaml.SequenceNode
		seq.Tag = "!!seq"
		seq.Value = ""
		seq.Style = yaml.FlowStyle
	}

	seq.Content = seq.Content[:0]
	for _, v := range values {
		seq.Content = append(seq.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	return nil
}

// mappingChild returns the mapping node stored under key in parent, creating an
// empty mapping (and the key) when it does not yet exist.
func mappingChild(parent *yaml.Node, key string) *yaml.Node {
	if v := mapValue(parent, key); v != nil {
		// A bare "key:" parses as a null scalar; appending to it would emit garbage.
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			v.Kind = yaml.MappingNode
			v.Tag = "!!map"
			v.Value = ""
		}
		return v
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valNode)
	return valNode
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalar sets key to a scalar value+tag in a mapping node, updating in place
// if the key already exists, otherwise appending it.
//
// It clears the style the old value had. A hand-quoted "false" that keeps its
// quotes becomes the string "true", and the manifest then fails to load with a
// type error that no devstack command can repair.
func setScalar(m *yaml.Node, key, value, tag string) {
	if v := mapValue(m, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = tag
		v.Value = value
		v.Style = 0
		return
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	m.Content = append(m.Content, keyNode, valNode)
}

// deleteKey removes key (and its value) from a mapping node if present.
func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func boolValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
