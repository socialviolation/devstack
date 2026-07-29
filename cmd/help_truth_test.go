package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// walkCommands visits every registered command, including hidden ones.
func walkCommands(root *cobra.Command, visit func(*cobra.Command)) {
	visit(root)
	for _, c := range root.Commands() {
		walkCommands(c, visit)
	}
}

// .devstack.json is the retired project store; the README tells you to delete
// it. Help that still names it sends you to edit a file devstack no longer
// reads. The code that migrates the legacy file is a separate thing and is
// meant to keep mentioning it.
func TestHelpDoesNotSendYouToTheRetiredStore(t *testing.T) {
	var offenders []string
	walkCommands(rootCmd, func(c *cobra.Command) {
		for label, text := range map[string]string{"short": c.Short, "long": c.Long, "example": c.Example} {
			if strings.Contains(text, ".devstack.json") {
				offenders = append(offenders, c.CommandPath()+" ("+label+")")
			}
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Usage, ".devstack.json") {
				offenders = append(offenders, c.CommandPath()+" --"+f.Name)
			}
		})
	})
	if len(offenders) > 0 {
		t.Errorf("help still points at the retired .devstack.json: %s", strings.Join(offenders, ", "))
	}
}

// A documented environment variable that nothing binds is a knob the reader
// will set and watch do nothing. DEVSTACK_DAEMON_PORT was one.
func TestDocumentedEnvVarsAreBound(t *testing.T) {
	// The bindings are installed by cobra's OnInitialize, which no unit test
	// goes through.
	initConfig()

	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Skipf("no README to check: %v", err)
	}
	rows := regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]+)` \\|").FindAllStringSubmatch(string(readme), -1)
	if len(rows) < 3 {
		t.Fatalf("found %d documented variables, so this guard proves nothing", len(rows))
	}

	// OTELCOL_BIN is read straight from the environment by the collector
	// plumbing rather than through viper, so binding is not the test for it.
	readDirectly := map[string]bool{"OTELCOL_BIN": true}

	for _, row := range rows {
		name := row[1]
		if readDirectly[name] {
			continue
		}
		t.Setenv(name, "sentinel-"+name)
		if !viperHasValue(name) {
			t.Errorf("README documents %s, but no devstack setting reads it", name)
		}
	}
}

// viperHasValue reports whether any bound key picks up the environment
// variable's value, whatever internal key it hides behind.
func viperHasValue(envName string) bool {
	want := "sentinel-" + envName
	for _, key := range viper.AllKeys() {
		if viper.GetString(key) == want {
			return true
		}
	}
	return false
}
