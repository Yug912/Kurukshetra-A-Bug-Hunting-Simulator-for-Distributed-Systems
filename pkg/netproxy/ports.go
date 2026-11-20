package netproxy

import (
	"runtime"
	"strings"
)

// PortReserver, when set (the engine sets it to docker.ReservePort), is told
// about every port the proxy listens on, so the Docker controller never
// publishes a container port on the same number.
var PortReserver func(port int)

func reservePort(p int) {
	if PortReserver != nil {
		PortReserver(p)
	}
}

// shadowingPossible reports whether this OS lets a socket on 127.0.0.1:P
// coexist with one on 0.0.0.0:P (BSD-style stacks such as macOS do; Linux
// refuses the second bind). Only then is the unshadowed-port probe needed.
var shadowingPossible = runtime.GOOS == "darwin" || strings.HasSuffix(runtime.GOOS, "bsd")
