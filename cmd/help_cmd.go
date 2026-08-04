package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// The top-level screen teaches the flow of the tool: cut a stack, run it, look
// at it, tear it down. cobra's own listing sorts every noun into one alphabetic
// column, which says what devstack holds and never what to do with it. A
// cobra.Group listing cannot carry these rows either: each row is a whole
// invocation with its arguments and its flag, and cobra renders a command name
// and its Short.
type helpRow struct {
	cmd  string
	what string
}

type helpGroup struct {
	title string
	rows  []helpRow
}

var helpGroups = []helpGroup{
	{"WORK ON A FEATURE", []helpRow{
		{"devstack stack create <name> --repos <svc|group>", "cut a stack for the feature"},
		{"devstack stack up <name>", "run it"},
		{"devstack status", "what runs, and where"},
		{"devstack service restart <svc> --stack <name>", "reload one copy"},
		{"devstack stack note <name> --add \"...\"", "where the work got to"},
		{"devstack stack rm <name>", "tear it down"},
	}},
	{"POINT IT SOMEWHERE", []helpRow{
		{"devstack env use <name> --stack <name>", "which database, which endpoints"},
	}},
	{"SET UP THIS MACHINE", []helpRow{
		{"devstack workspace add <path>", "register your services"},
		{"devstack workspace up", "start the daemon, and base"},
		{"devstack upgrade", "new devstack, migrated"},
	}},
}

var moreRow = helpRow{"devstack help more", "otel, tunnel, ports, hooks, deps"}

// moreCommands are real and are needed now and then. They are not part of
// learning the tool, so they leave the first screen and keep everything else.
var moreCommands = []helpRow{
	{"otel", "traces and logs: query them, and run the collector"},
	{"tunnel", "forward this workspace's ports over SSH"},
	{"ports", "see which process holds a port, and take the port back"},
	{"dependencies", "declare which service starts before which"},
	{"hooks", "run an action when a stack or a service changes state"},
	{"group", "act on a named set of services at one time"},
}

var helpCmd = &cobra.Command{
	Use:   "help [command]",
	Short: "Show the help of one command",
	Long: `Show the help of one command. With no command, devstack shows the first screen.

To read the commands that the first screen leaves out, run: devstack help more`,
	Run: func(c *cobra.Command, args []string) {
		target, _, err := rootCmd.Find(args)
		if err != nil || target == nil {
			fmt.Fprintf(c.OutOrStdout(), "There is no command %q. For the list of commands, run: devstack --help\n", strings.Join(args, " "))
			return
		}
		target.InitDefaultHelpFlag()
		_ = target.Help()
	},
}

var helpMoreCmd = &cobra.Command{
	Use:   "more",
	Short: "List the commands that the first screen leaves out",
	Run: func(c *cobra.Command, args []string) {
		fmt.Fprint(c.OutOrStdout(), moreScreen())
	},
}

func init() {
	rootCmd.AddCommand(helpCmd)
	helpCmd.AddCommand(helpMoreCmd)
	rootCmd.SetHelpCommand(helpCmd)
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
}

func moreScreen() string {
	var b strings.Builder
	b.WriteString("These commands are not part of the daily flow. Each one works as it did before,\n")
	b.WriteString("and each one keeps its own help.\n\n")
	for _, r := range moreCommands {
		fmt.Fprintf(&b, "  devstack %-14s %s\n", r.cmd, r.what)
	}
	b.WriteString("\nFor the full help of one command, run: devstack <command> --help\n")
	return b.String()
}

func workflowScreen() string {
	var b strings.Builder
	for _, g := range helpGroups {
		fmt.Fprintf(&b, "%s\n", g.title)
		for _, r := range g.rows {
			fmt.Fprintf(&b, "  %-50s %s\n", r.cmd, r.what)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  %-50s %s\n", moreRow.cmd, moreRow.what)
	return b.String()
}

// installHelp gives the root command the workflow screen, and keeps every other
// command on cobra's own screen. cobra reads the help template from the parent
// when a command has none, so each child of the root gets the default template
// here. The commands are added by the init functions of many files, so this runs
// at execution and not in an init function.
func installHelp() {
	// cobra adds these two at execution, which is after this loop, and a command
	// added later would inherit the root's screen.
	rootCmd.InitDefaultCompletionCmd()
	rootCmd.InitDefaultHelpCmd()
	for _, c := range rootCmd.Commands() {
		c.SetHelpTemplate(defaultHelpTemplate)
	}
	rootCmd.SetHelpTemplate("{{with .Long}}{{. | trimTrailingWhitespaces}}\n\n{{end}}" + workflowScreen() +
		"\nFlags:\n{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}\n\nFor the full help of one command, run: devstack <command> --help\n")
}

var defaultHelpTemplate = (&cobra.Command{}).HelpTemplate()
