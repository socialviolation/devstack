package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

var LegacyEdgeKeys = []string{"dependencies", "startsAfter", "calls"}

func SetShared(workspacePath string, names []string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, n := range names {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: n})
		}
		if v := mapValue(root, "shared"); v != nil {
			*v = *seq
			return nil
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "shared"}
		at := len(root.Content)
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "groups" {
				at = i
				break
			}
		}
		root.Content = append(root.Content[:at], append([]*yaml.Node{key, seq}, root.Content[at:]...)...)
		return nil
	})
}

func RemoveLegacyEdgeMaps(workspacePath string) error {
	return editWorkspaceManifest(workspacePath, func(root *yaml.Node) error {
		for _, key := range LegacyEdgeKeys {
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i].Value != key {
					continue
				}
				foot := trailingFootComment(root.Content[i], root.Content[i+1])
				root.Content = append(root.Content[:i], root.Content[i+2:]...)
				if foot != "" && i < len(root.Content) {
					next := root.Content[i]
					if next.HeadComment == "" {
						next.HeadComment = foot
					} else {
						next.HeadComment = foot + "\n" + next.HeadComment
					}
				}
				break
			}
		}
		return nil
	})
}

func trailingFootComment(key, value *yaml.Node) string {
	nodes := []*yaml.Node{}
	if value.Kind == yaml.MappingNode && len(value.Content) >= 2 {
		nodes = append(nodes, value.Content[len(value.Content)-2], value.Content[len(value.Content)-1])
	}
	nodes = append(nodes, value, key)
	var parts []string
	for _, n := range nodes {
		if n.FootComment != "" {
			parts = append(parts, n.FootComment)
		}
	}
	return strings.Join(parts, "\n")
}

func AddServiceEdges(manifestPath string, calls, needs []string) error {
	return editManifest(manifestPath, func(root *yaml.Node) error {
		needsSeq, err := edgeList(root, "needs", len(needs) > 0)
		if err != nil {
			return err
		}
		callsSeq, err := edgeList(root, "calls", len(calls) > 0)
		if err != nil {
			return err
		}
		for _, n := range needs {
			if indexEdge(needsSeq, n) >= 0 {
				continue
			}
			node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: n}
			if i := indexEdge(callsSeq, n); i >= 0 {
				node = callsSeq.Content[i]
				callsSeq.Content = append(callsSeq.Content[:i], callsSeq.Content[i+1:]...)
			}
			needsSeq.Content = append(needsSeq.Content, node)
		}
		for _, c := range calls {
			if indexEdge(needsSeq, c) >= 0 || indexEdge(callsSeq, c) >= 0 {
				continue
			}
			callsSeq.Content = append(callsSeq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: c})
		}
		if callsSeq != nil && len(callsSeq.Content) == 0 {
			deleteKey(root, "calls")
		}
		return nil
	})
}

func edgeList(root *yaml.Node, key string, create bool) (*yaml.Node, error) {
	seq := mapValue(root, key)
	if seq == nil {
		if !create {
			return nil, nil
		}
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
		return seq, nil
	}
	if seq.Kind == yaml.ScalarNode && seq.Tag == "!!null" {
		seq.Kind, seq.Tag, seq.Value = yaml.SequenceNode, "!!seq", ""
	}
	if seq.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s is not a list", key)
	}
	return seq, nil
}

func indexEdge(seq *yaml.Node, svc string) int {
	if seq == nil {
		return -1
	}
	for i, item := range seq.Content {
		name := item.Value
		if item.Kind == yaml.MappingNode {
			if v := mapValue(item, "service"); v != nil {
				name = v.Value
			}
		}
		if name == svc {
			return i
		}
	}
	return -1
}
