//go:build unix

package cmd

import (
	"net"
	"os"
	"strings"
)

// sdNotify sends a state change to the service manager, as sd_notify(3)
// defines it: newline-separated VAR=VALUE lines on the datagram socket named
// by NOTIFY_SOCKET.
//
// Hand-rolled rather than taken as a dependency, because the whole protocol is
// a few lines written to one socket and a systemd binding would cost more to
// justify than to write. Every part of it is best-effort: no NOTIFY_SOCKET
// means nobody is listening, and a socket that will not take a status line
// must never be the reason a sync daemon stops syncing.
func sdNotify(state string) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}
	// A leading '@' names a socket in the abstract namespace, which Go spells
	// with a leading NUL instead.
	if strings.HasPrefix(sock, "@") {
		sock = "\x00" + sock[1:]
	}
	c, err := net.Dial("unixgram", sock)
	if err != nil {
		return
	}
	defer c.Close()
	c.Write([]byte(state))
}
