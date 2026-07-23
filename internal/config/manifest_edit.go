package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// editWorkspaceManifest applies mutate to the manifest's root mapping node and
// writes the result back, preserving comments and unrelated fields. It operates
// on the raw YAML node tree rather than the typed struct so hand-authored
// formatting survives the round-trip.
func editWorkspaceManifest(workspacePath string, mutate func(root *yaml.Node) error) error {
	if !HasWorkspaceManifest(workspacePath) {
		return fmt.Errorf("no %s in %s — config lives in the workspace manifest", WorkspaceManifestFileName, workspacePath)
	}
	return editManifest(WorkspaceManifestPath(workspacePath), mutate)
}

// editManifest applies mutate to the manifest's root mapping node at path and
// writes the result back.
func editManifest(path string, mutate func(root *yaml.Node) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read manifest %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse manifest %s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("manifest %s is not a mapping", path)
	}

	if err := mutate(doc.Content[0]); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("failed to encode manifest %s: %w", path, err)
	}
	enc.Close()

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write manifest %s: %w", path, err)
	}
	return nil
}

// SetServiceEnvValue writes key=value into the service manifest's env.values at
// repoPath, creating the env.values block if absent. It preserves comments and
// unrelated fields.
//
// env.values is committed to git: callers must not route secrets here.
func SetServiceEnvValue(repoPath, key, value string) error {
	if !HasServiceManifest(repoPath) {
		return fmt.Errorf("no %s in %s", ServiceManifestFileName, repoPath)
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
			return fmt.Errorf("environment %q is not defined in %s", envName, WorkspaceManifestFileName)
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
		return fmt.Errorf("no %s in %s", ServiceManifestFileName, repoPath)
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
// the key so the default (signoz) applies.
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
func setScalar(m *yaml.Node, key, value, tag string) {
	if v := mapValue(m, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = tag
		v.Value = value
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
