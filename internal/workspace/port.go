package workspace

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ResolvePort returns the port Tilt is actually running on for the named workspace.
// It reads the registry first, then cross-checks against the running process via
// the PID file. If drift is detected the registry is updated in-place.
// Returns 0 if the workspace is not found.
func ResolvePort(wsName string) int {
	ws, err := FindByName(wsName)
	if err != nil {
		ws2, err2 := FindByPath(wsName)
		if err2 != nil {
			return 0
		}
		ws = ws2
	}

	actual := portFromPID(ws.Name)
	if actual != 0 && actual != ws.TiltPort {
		_ = UpdatePort(ws.Name, actual)
		return actual
	}

	return ws.TiltPort
}

// portFromPID reads the PID file for a workspace and extracts the --port value
// from the running Tilt process's command line. Returns 0 if not determinable.
func portFromPID(wsName string) int {
	pidFile := PIDFile(wsName)
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return 0
	}
	args := strings.Split(string(data), "\x00")
	for i, arg := range args {
		if arg == "--port" && i+1 < len(args) {
			if p, err := strconv.Atoi(args[i+1]); err == nil {
				return p
			}
		}
		if strings.HasPrefix(arg, "--port=") {
			if p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil {
				return p
			}
		}
	}
	return 0
}
