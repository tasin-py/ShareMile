package main

import (
	"context"
	"net"
	"syscall"
)

// listenUDPShared listens on a UDP port that other programs (another copy of
// ShareMile) may listen on too; broadcasts reach all of them.
func listenUDPShared(port int) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
		return serr
	}}
	return lc.ListenPacket(context.Background(), "udp4", ":"+itoa(port))
}
