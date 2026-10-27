package mcp

import (
	"net"
	"os"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestMain(m *testing.M) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	workspace.HostTiltPort = l.Addr().(*net.TCPAddr).Port
	l.Close()
	os.Exit(m.Run())
}
