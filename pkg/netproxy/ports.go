package netproxy

// PortReserver, when set (the engine sets it to docker.ReservePort), is told
// about every port the proxy listens on, so the Docker controller never
// publishes a container port on the same number.
var PortReserver func(port int)

func reservePort(p int) {
	if PortReserver != nil {
		PortReserver(p)
	}
}
