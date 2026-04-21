package workspace

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type SessionState struct {
	SessionID     string   `json:"session_id"`
	WorkspaceName string   `json:"workspace_name"`
	WorkspacePath string   `json:"workspace_path"`
	SupervisorPID int      `json:"supervisor_pid"`
	TiltPort      int      `json:"tilt_port"`
	ActivePorts   []int    `json:"active_ports,omitempty"`
	Status        string   `json:"status"`
	StartedAt     string   `json:"started_at"`
	ClosedAt      string   `json:"closed_at,omitempty"`
	Residue       []string `json:"residue,omitempty"`
}

func SessionFile(name string) string {
	return filepath.Join(DataDir(name), "session.json")
}

func SaveSession(state *SessionState) error {
	if err := os.MkdirAll(filepath.Dir(SessionFile(state.WorkspaceName)), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(SessionFile(state.WorkspaceName), append(data, '\n'), 0644)
}

func LoadSession(name string) (*SessionState, error) {
	data, err := os.ReadFile(SessionFile(name))
	if err != nil {
		return nil, err
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func OpenSession(ws *Workspace, pid int, activePorts []int) (*SessionState, error) {
	state := &SessionState{
		SessionID:     fmt.Sprintf("%s-%d", ws.Name, time.Now().UnixNano()),
		WorkspaceName: ws.Name,
		WorkspacePath: ws.Path,
		SupervisorPID: pid,
		TiltPort:      ws.TiltPort,
		ActivePorts:   activePorts,
		Status:        "open",
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	return state, SaveSession(state)
}

func CloseSession(name string, residue []string) error {
	state, err := LoadSession(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(residue) > 0 {
		state.Status = "unclean"
		state.Residue = residue
	} else {
		state.Status = "closed"
		state.Residue = nil
	}
	state.ClosedAt = time.Now().UTC().Format(time.RFC3339)
	return SaveSession(state)
}

func DetectResidue(pid int, ports []int) []string {
	var residue []string
	if pid > 0 && processAlive(pid) {
		residue = append(residue, fmt.Sprintf("process %d still running", pid))
	}
	for _, port := range ports {
		if portListening(port) {
			residue = append(residue, fmt.Sprintf("port %d still listening", port))
		}
	}
	return residue
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
