//go:build linux

package gateway

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// When SOCKS clients do not announce a UDP port, bind the association to the
// process owning the authenticated TCP client. Another local process must not
// claim a loopback UDP relay by sending the first packet.
func udpControlOwner(conn net.Conn) (uint32, bool) {
	peer, peerOK := conn.RemoteAddr().(*net.TCPAddr)
	local, localOK := conn.LocalAddr().(*net.TCPAddr)
	if !peerOK || !localOK {
		return 0, false
	}
	tables := map[string]func([]string) bool{}
	for _, ipv6 := range []bool{false, true} {
		peerKey, peerOK := procSocketAddress(peer.IP, peer.Port, ipv6)
		localKey, localOK := procSocketAddress(local.IP, local.Port, ipv6)
		if !peerOK || !localOK {
			continue
		}
		path := "/proc/net/tcp"
		if ipv6 {
			path += "6"
		}
		tables[path] = func(fields []string) bool {
			return fields[1] == peerKey && fields[2] == localKey
		}
	}
	uid, inode, found := procSocketIdentity(tables)
	if !found {
		return 0, false
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, entry := range processes {
		pid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid {
			continue
		}
		if processOwnsSocket(uint32(pid), inode) {
			return uint32(pid), true
		}
	}
	return 0, false
}

func udpSourceOwned(source *net.UDPAddr, owner uint32) bool {
	tables := map[string]func([]string) bool{}
	for _, ipv6 := range []bool{false, true} {
		key, ok := procSocketAddress(source.IP, source.Port, ipv6)
		if !ok {
			continue
		}
		wildcard := fmt.Sprintf("00000000:%04X", source.Port)
		path := "/proc/net/udp"
		if ipv6 {
			wildcard = fmt.Sprintf("%032X:%04X", 0, source.Port)
			path += "6"
		}
		tables[path] = func(fields []string) bool {
			return fields[1] == key || fields[1] == wildcard
		}
	}
	_, inode, ok := procSocketIdentity(tables)
	return ok && processOwnsSocket(owner, inode)
}

func processOwnsSocket(pid uint32, inode string) bool {
	directory := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink(directory + "/" + entry.Name())
		if err == nil && target == "socket:["+inode+"]" {
			return true
		}
	}
	return false
}

func procSocketAddress(ip net.IP, port int, ipv6 bool) (string, bool) {
	if ipv6 {
		ip = ip.To16()
	} else {
		ip = ip.To4()
	}
	if ip == nil {
		return "", false
	}
	var address strings.Builder
	for offset := 0; offset < len(ip); offset += 4 {
		fmt.Fprintf(&address, "%08X", binary.LittleEndian.Uint32(ip[offset:offset+4]))
	}
	fmt.Fprintf(&address, ":%04X", port)
	return address.String(), true
}

func procSocketIdentity(tables map[string]func([]string) bool) (uint32, string, bool) {
	var owner uint32
	var inode string
	found := false
	for path, matches := range tables {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue // IPv6 tables are absent when IPv6 is disabled.
		}
		if err != nil {
			return 0, "", false
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || !matches(fields) {
				continue
			}
			uid, err := strconv.ParseUint(fields[7], 10, 32)
			if err != nil || fields[9] == "0" || (found && inode != fields[9]) {
				return 0, "", false
			}
			owner, found = uint32(uid), true
			inode = fields[9]
		}
	}
	return owner, inode, found
}
