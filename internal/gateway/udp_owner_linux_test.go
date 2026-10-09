//go:build linux

package gateway

import (
	"net"
	"os"
	"testing"
)

func TestUDPWildcardSocketOwner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	control, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	owner, ok := udpControlOwner(control)
	if !ok {
		t.Fatal("cannot determine TCP owner")
	}
	// Go can use a dual-stack IPv6 socket for an IPv4 wildcard address.
	// Xray uses the same kind of socket for its local SOCKS UDP outbound.
	source, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	observed := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: source.LocalAddr().(*net.UDPAddr).Port}
	if !udpSourceOwned(observed, owner) {
		t.Fatalf("authenticated wildcard socket rejected: %s", source.LocalAddr())
	}
	// Adjacent IDs can be threads of this same process on Linux. Use the
	// parent, which cannot own a socket created after this process started.
	if udpSourceOwned(observed, uint32(os.Getppid())) {
		t.Fatal("another process accepted for wildcard socket")
	}
}
