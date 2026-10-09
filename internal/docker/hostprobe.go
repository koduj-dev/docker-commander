package docker

import (
	"net"
	"strings"
)

// hostIface is one network interface on a Docker host, as far as the
// diagnostics checks need it: its subnets, its MTU, and whether it looks like
// the interface the host actually routes through.
type hostIface struct {
	Name      string
	Subnets   []string // CIDR strings
	MTU       int      // 0 = unknown
	IsDefault bool
}

// hostProbeResult is what probing a host's network interfaces produced —
// either a usable interface list, or (on a remote host) an honest record of
// what was tried and why it failed, so a check can report "skipped: X" rather
// than silently guessing or dropping the row.
type hostProbeResult struct {
	Method    string // "local", "ip -j addr", "ip addr", "ifconfig", "ipconfig", or "" if everything failed
	Ifaces    []hostIface
	Attempted []string
	Err       error
}

// dockerOwnedIface reports whether an interface name looks like one Docker
// itself created (a bridge, or one half of a veth pair) — these trivially
// "overlap" the network they're the gateway for, which is not the bug the
// network/host-overlap check exists to catch, so they're excluded from it.
func dockerOwnedIface(name string) bool {
	return name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth")
}

// localHostProbe reads this process's own network interfaces via the Go
// stdlib — no shelling out, so it works identically on Linux, macOS and
// Windows. The "default" interface is guessed with the standard trick of
// dialing UDP to an address in a documentation-reserved range (TEST-NET-3,
// RFC 5737): no packet is ever sent for a UDP socket that's never written to,
// this only resolves which local interface the kernel would route through.
func localHostProbe() hostProbeResult {
	ifaces, err := net.Interfaces()
	if err != nil {
		return hostProbeResult{Attempted: []string{"net.Interfaces"}, Err: err}
	}

	defaultIP := ""
	if conn, err := net.Dial("udp", "203.0.113.1:80"); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			defaultIP = addr.IP.String()
		}
		_ = conn.Close()
	}

	var out []hostIface
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		hi := hostIface{Name: ifi.Name, MTU: ifi.MTU}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			hi.Subnets = append(hi.Subnets, ipnet.String())
			if defaultIP != "" && ipnet.IP.String() == defaultIP {
				hi.IsDefault = true
			}
		}
		if len(hi.Subnets) > 0 {
			out = append(out, hi)
		}
	}
	return hostProbeResult{Method: "local", Ifaces: out}
}
