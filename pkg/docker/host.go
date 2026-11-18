package docker

import (
	"os"
	"path/filepath"
)

// discoverHost returns a docker host URL when DOCKER_HOST is unset and the
// default /var/run/docker.sock is missing, by probing well-known sockets.
func discoverHost() string {
	if os.Getenv("DOCKER_HOST") != "" {
		return ""
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".colima", "default", "docker.sock"),
		filepath.Join(home, ".docker", "run", "docker.sock"),
		filepath.Join(home, ".orbstack", "run", "docker.sock"),
		filepath.Join(home, ".rd", "docker.sock"),
	} {
		if _, err := os.Stat(p); err == nil {
			return "unix://" + p
		}
	}
	return ""
}
