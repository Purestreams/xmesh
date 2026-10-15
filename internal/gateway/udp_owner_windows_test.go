//go:build windows

package gateway

import (
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsControlOwnerIsClientProcess(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestWindowsControlOwnerHelper$")
	cmd.Env = append(os.Environ(), "XMESH_TCP_OWNER_HELPER="+listener.Addr().String())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	control, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	owner, ok := udpControlOwner(control)
	if !ok || owner != uint32(cmd.Process.Pid) {
		t.Fatalf("owner=%d ok=%v; want client PID %d, not Gateway PID %d", owner, ok, cmd.Process.Pid, os.Getpid())
	}
}

func TestWindowsControlOwnerHelper(t *testing.T) {
	address := os.Getenv("XMESH_TCP_OWNER_HELPER")
	if address == "" {
		t.Skip("subprocess fixture")
	}
	conn, err := net.DialTimeout("tcp4", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.Copy(io.Discard, conn)
}
