// Package netutil holds small network helpers shared by setup and handlers.
package netutil

import (
	"fmt"
	"net"
)

// PortFree reports whether a TCP listener can bind port on all interfaces.
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
