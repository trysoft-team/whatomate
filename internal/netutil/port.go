// Package netutil holds small network helpers shared by setup and handlers.
package netutil

import (
	"net"
	"strconv"
)

// PortFree reports whether a TCP listener can bind port on all interfaces
// without sharing it with another program's listener. Go sets SO_REUSEADDR on
// Unix, so on macOS/BSD a dual-stack wildcard bind succeeds beside a listener
// on 0.0.0.0, 127.0.0.1 or ::1 alone; each address is therefore tried
// separately.
func PortFree(port int) bool {
	p := strconv.Itoa(port)
	if !canListen("tcp", ":"+p) || !canListen("tcp4", "0.0.0.0:"+p) || !canListen("tcp", "127.0.0.1:"+p) {
		return false
	}
	// A failed IPv6 bind only means "taken" when the host has that IPv6 address.
	if !canListen("tcp6", "[::]:"+p) && canListen("tcp6", "[::]:0") {
		return false
	}
	return canListen("tcp", "[::1]:"+p) || !canListen("tcp", "[::1]:0")
}

func canListen(network, addr string) bool {
	ln, err := net.Listen(network, addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
