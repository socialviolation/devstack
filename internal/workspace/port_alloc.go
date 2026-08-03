package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const servicePortBase = 20000
const servicePortScanLimit = 10000

// portsFile returns the per-stack service-port allocation record, keyed by name
// beside the stack's other runtime state under DataDir.
func portsFile(stackName string) string {
	return DataDir(stackName) + "ports.json"
}

// portAllocLockPath returns the single shared allocation lockfile. It lives at
// DataRoot rather than in a per-stack dir because allocation spans every stack:
// selecting a free port reads all stacks' records, so one process-wide lock must
// serialise the whole read-compute-write across all callers.
func portAllocLockPath() string {
	return filepath.Join(DataRoot(), "ports.lock")
}

// withPortAllocLock runs fn while holding an exclusive advisory lock on the
// shared allocation lockfile, serialising concurrent AllocatePorts across
// processes and goroutines so two callers never select the same port.
func withPortAllocLock(fn func() error) error {
	lockPath := portAllocLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("failed to create data root: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open port lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to lock ports: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// LoadPorts reads a stack's persisted service-port allocation, returning an
// empty map when the stack has none.
func LoadPorts(stackName string) (map[string]int, error) {
	data, err := os.ReadFile(portsFile(stackName))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("failed to read port allocation: %w", err)
	}
	var ports map[string]int
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil, fmt.Errorf("failed to parse port allocation: %w", err)
	}
	if ports == nil {
		ports = map[string]int{}
	}
	return ports, nil
}

// savePorts persists a stack's allocation map, creating its data dir if needed.
func savePorts(stackName string, ports map[string]int) error {
	if err := os.MkdirAll(DataDir(stackName), 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}
	data, err := json.MarshalIndent(ports, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal port allocation: %w", err)
	}
	if err := os.WriteFile(portsFile(stackName), data, 0644); err != nil {
		return fmt.Errorf("failed to write port allocation: %w", err)
	}
	return nil
}

// ReleasePorts deletes a stack's allocation record, freeing its ports.
func ReleasePorts(stackName string) error {
	if err := os.Remove(portsFile(stackName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to release port allocation: %w", err)
	}
	return nil
}

// allocatedPorts returns every port held by any stack's record under DataRoot,
// so an allocation skips ports owned by other stacks. It reads the data root
// rather than the registry because ownership lives in the per-stack records.
func allocatedPorts() (map[int]bool, error) {
	used := map[int]bool{}
	entries, err := os.ReadDir(DataRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return used, nil
		}
		return nil, fmt.Errorf("failed to read data root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ports, err := LoadPorts(e.Name())
		if err != nil {
			return nil, err
		}
		for _, p := range ports {
			used[p] = true
		}
	}
	return used, nil
}

// AllocatePorts allocates one free localhost port per key, persists the map for
// this stack, and returns it. A candidate is rejected if it is currently
// listening, a registered TiltPort, or already held by any stack. The whole
// operation runs under the shared allocation lock, so concurrent callers never
// select the same port.
func AllocatePorts(stackName string, keys []string) (map[string]int, error) {
	var result map[string]int
	err := withPortAllocLock(func() error {
		out, err := allocateKeys(nil, keys)
		if err != nil {
			return err
		}
		if err := savePorts(stackName, out); err != nil {
			return err
		}
		result = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// AllocateAdditionalPorts extends a stack's allocation with the keys it does not
// already hold and returns the union, leaving every port it already holds on the
// same number.
//
// Adding a service to a live stack cannot go through AllocatePorts: that saves
// exactly the keys it was handed, and every port on the machine is reserved
// against it, so re-allocating a stack's whole key set moves every one of its
// running services to a different port.
func AllocateAdditionalPorts(stackName string, keys []string) (map[string]int, error) {
	var result map[string]int
	err := withPortAllocLock(func() error {
		held, err := LoadPorts(stackName)
		if err != nil {
			return err
		}
		var missing []string
		for _, key := range keys {
			if _, ok := held[key]; !ok {
				missing = append(missing, key)
			}
		}
		out, err := allocateKeys(held, missing)
		if err != nil {
			return err
		}
		if err := savePorts(stackName, out); err != nil {
			return err
		}
		result = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// allocateKeys returns held plus one free localhost port per key. Callers hold
// the allocation lock.
func allocateKeys(held map[string]int, keys []string) (map[string]int, error) {
	workspaces, err := Load()
	if err != nil {
		return nil, err
	}
	reserved := map[int]bool{}
	for _, ws := range workspaces {
		if ws.TiltPort != 0 {
			reserved[ws.TiltPort] = true
		}
	}
	allocated, err := allocatedPorts()
	if err != nil {
		return nil, err
	}
	for p := range allocated {
		reserved[p] = true
	}

	out := make(map[string]int, len(held)+len(keys))
	for k, v := range held {
		out[k] = v
	}
	candidate := servicePortBase
	limit := servicePortBase + servicePortScanLimit
	for _, key := range keys {
		for {
			if candidate >= limit {
				return nil, fmt.Errorf("no free service port found in range %d-%d", servicePortBase, limit-1)
			}
			c := candidate
			candidate++
			if reserved[c] || portInUse(c) {
				continue
			}
			reserved[c] = true
			out[key] = c
			break
		}
	}
	return out, nil
}
