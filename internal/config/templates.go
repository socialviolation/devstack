package config

import (
	"fmt"
	"sort"
	"strings"
)

func (m *WorkspaceManifest) TemplateNames() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.Templates))
	for n := range m.Templates {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (rw *ResolvedWorkspace) validateTemplates() error {
	if rw.Manifest.Partial {
		return nil
	}
	for _, name := range rw.Manifest.TemplateNames() {
		t := rw.Manifest.Templates[name]
		if _, ok := rw.Services[name]; ok {
			return fmt.Errorf("the template %q has the same name as a service. Give the template a different name", name)
		}
		if _, ok := rw.Manifest.Groups[name]; ok {
			return fmt.Errorf("the template %q has the same name as a group. Give the template a different name", name)
		}
		if len(t.Services) == 0 {
			return fmt.Errorf("the template %q names no services. Add services or groups to it", name)
		}
		for _, s := range t.Services {
			_, isService := rw.Services[s]
			_, isGroup := rw.Manifest.Groups[s]
			if !isService && !isGroup {
				return fmt.Errorf("the template %q names %q. The workspace does not declare a service or a group with that name", name, s)
			}
		}
		if t.Env != "" {
			if _, ok := rw.Manifest.Environments[t.Env]; !ok {
				envs := make([]string, 0, len(rw.Manifest.Environments))
				for e := range rw.Manifest.Environments {
					envs = append(envs, e)
				}
				sort.Strings(envs)
				return fmt.Errorf("the template %q uses the environment %q. The workspace does not declare it. Environments: %s", name, t.Env, strings.Join(envs, ", "))
			}
		}
	}
	return nil
}
