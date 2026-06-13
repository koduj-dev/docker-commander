package mcp

import (
	"fmt"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// portString renders a port mapping compactly, e.g. "0.0.0.0:8080->80/tcp" or
// "80/tcp" when the port is not published.
func portString(p docker.PortMapping) string {
	if p.PublicPort != 0 {
		host := p.IP
		if host == "" {
			host = "0.0.0.0"
		}
		return fmt.Sprintf("%s:%d->%d/%s", host, p.PublicPort, p.PrivatePort, p.Type)
	}
	return fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
}

// mountString renders a mount as "<type> <source>-><destination> <ro|rw>". This
// is metadata (paths and mode) only — it never exposes file contents.
func mountString(m docker.MountInfo) string {
	mode := "rw"
	if !m.RW {
		mode = "ro"
	}
	return fmt.Sprintf("%s %s->%s %s", m.Type, m.Source, m.Destination, mode)
}
