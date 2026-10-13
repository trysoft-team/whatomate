// Package netutil holds small network helpers shared by setup and handlers.
package netutil

import (
	"net"
	"strconv"
)

// PortFree reports whether a TCP listener can bind port on all interfaces
// without sharing it with another program's loopback-only listener. Go sets
// SO_REUSEADDR on Unix, so on macOS/BSD a wildcard bind succeeds beside a
// listener on 127.0.0.1 or ::1; each address is therefore tried separately.
func PortFree(port int) bool {
	p := strconv.Itoa(port)
	if !canListen(":"+p) || !canListen("127.0.0.1:"+p) {
		return false
	}
	// A failed ::1 bind only means "taken" when the host has IPv6 loopback.
	return canListen("[::1]:"+p) || !canListen("[::1]:0")
}

func canListen(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
