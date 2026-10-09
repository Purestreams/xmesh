//go:build windows

package gateway

import (
	"encoding/binary"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ipHelper = windows.NewLazySystemDLL("iphlpapi.dll")

// Windows socket tables provide a process identity rather than a Unix UID.
func udpControlOwner(conn net.Conn) (uint32, bool) {
	peer, peerOK := conn.RemoteAddr().(*net.TCPAddr)
	local, localOK := conn.LocalAddr().(*net.TCPAddr)
	if !peerOK || !localOK {
		return 0, false
	}
	table, ok := ownerTable("GetExtendedTcpTable", 5) // TCP_TABLE_OWNER_PID_ALL
	if !ok {
		return 0, false
	}
	for offset := 4; offset+24 <= len(table); offset += 24 {
		row := table[offset : offset+24]
		if net.IP(row[4:8]).Equal(peer.IP) && int(binary.BigEndian.Uint16(row[8:10])) == peer.Port && net.IP(row[12:16]).Equal(local.IP) && int(binary.BigEndian.Uint16(row[16:18])) == local.Port {
			pid := binary.LittleEndian.Uint32(row[20:24])
			return pid, pid != 0
		}
	}
	return 0, false
}

func udpSourceOwned(source *net.UDPAddr, owner uint32) bool {
	table, ok := ownerTable("GetExtendedUdpTable", 1) // UDP_TABLE_OWNER_PID
	if !ok {
		return false
	}
	found := false
	for offset := 4; offset+12 <= len(table); offset += 12 {
		row := table[offset : offset+12]
		ip := net.IP(row[:4])
		if (ip.IsUnspecified() || ip.Equal(source.IP)) && int(binary.BigEndian.Uint16(row[4:6])) == source.Port {
			if owner == 0 || binary.LittleEndian.Uint32(row[8:12]) != owner {
				return false
			}
			found = true
		}
	}
	return found
}

func ownerTable(name string, class uintptr) ([]byte, bool) {
	proc := ipHelper.NewProc(name)
	if err := proc.Find(); err != nil {
		return nil, false
	}
	var size uint32
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, 2, class, 0)
	if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 16<<20 {
		return nil, false
	}
	for attempt := 0; attempt < 3; attempt++ {
		buffer := make([]byte, size)
		status, _, _ = proc.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, 2, class, 0)
		if status == 0 {
			return buffer[:size], true
		}
		if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 16<<20 {
			return nil, false
		}
	}
	return nil, false
}
