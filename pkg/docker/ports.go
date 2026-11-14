package docker

import (
	"fmt"
	"net"
	"strconv"
	"sync"
)

var (
	portMu   sync.Mutex
	reserved = map[int]bool{}
)

// ReservePort marks a port as in use by this process so FreePort never returns it.
func ReservePort(p int) {
	portMu.Lock()
	reserved[p] = true
	portMu.Unlock()
}

// ReleasePort undoes ReservePort.
func ReleasePort(p int) {
	portMu.Lock()
	delete(reserved, p)
	portMu.Unlock()
}

// PortFreeEverywhere reports whether p can be bound on both 127.0.0.1 and the
// wildcard address. Checking both matters on macOS: a socket on 0.0.0.0:p and
// another one on 127.0.0.1:p can coexist (Go sets SO_REUSEADDR), and the more
// specific one silently wins for loopback traffic — which is how Colima/Docker
// Desktop deliver container traffic to the host.
func PortFreeEverywhere(p int) bool {
	for _, addr := range []string{"127.0.0.1", "0.0.0.0"} {
		l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(p)))
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// FreePort returns a TCP port that is free on 127.0.0.1 and 0.0.0.0 and not
// handed out before by this process. The port is reserved; callers release it
// with ReleasePort when done (leaking a reservation is harmless).
func FreePort() (int, error) {
	for i := 0; i < 100; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		portMu.Lock()
		taken := reserved[p]
		portMu.Unlock()
		if taken || !PortFreeEverywhere(p) {
			continue
		}
		ReservePort(p)
		return p, nil
	}
	return 0, fmt.Errorf("no free port found")
}
