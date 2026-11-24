package config

import (
	"fmt"
	"strings"
	"time"
)

const DefaultTaskTimeout = 10 * time.Minute

// ServiceTask is a named shell command a service declares under tasks. Run and
// the values of Env take the same ${service.field} references a hook takes.
type ServiceTask struct {
	Run     string            `yaml:"run"`
	WorkDir string            `yaml:"workDir,omitempty"`
	Timeout string            `yaml:"timeout,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

func (t ServiceTask) ResolvedTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(t.Timeout)
	if raw == "" {
		return DefaultTaskTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("the timeout %q is not a duration, such as 30s or 10m: %w", t.Timeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("the timeout %q must be more than zero", t.Timeout)
	}
	return d, nil
}
