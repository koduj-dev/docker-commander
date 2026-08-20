package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// probeCmdTimeout bounds each individual probe command — these are simple
// local commands (ip/ifconfig/ipconfig/df) that should return in well under a
// second on a healthy host; a hung shell must not hold up the whole battery.
const probeCmdTimeout = 8 * time.Second

// remoteHostProbe determines a host's network interfaces over SSH by trying
// commands in order — Linux's modern `ip` first, falling back through older
// Linux/BSD/macOS `ifconfig`, down to Windows' `ipconfig` — and parsing
// whichever one produces output. The user asked for this explicitly: Docker
// hosts show up as Ubuntu, Debian, other Linux distros, macOS and Windows, not
// just the one this code happens to run on.
func (m *Manager) remoteHostProbe(ctx context.Context, hostID int64, h *store.Host) hostProbeResult {
	var attempted []string
	var lastErr error

	type probe struct {
		method string
		cmd    string
		parse  func(string) ([]hostIface, error)
	}
	probes := []probe{
		{"ip -j addr", "ip -j addr show", parseIPAddrJSON},
		{"ip addr", "ip addr show", parseIPAddrText},
		{"ifconfig", "ifconfig -a", parseIfconfigText},
		{"ipconfig", "ipconfig /all", parseIpconfigText},
	}

	var ifaces []hostIface
	var method string
	for _, p := range probes {
		attempted = append(attempted, p.method)
		out, err := m.sshExec(ctx, hostID, h, p.cmd, probeCmdTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		parsed, err := p.parse(out)
		if err != nil || len(parsed) == 0 {
			if err != nil {
				lastErr = err
			}
			continue
		}
		ifaces, method = parsed, p.method
		break
	}
	if method == "" {
		if lastErr == nil {
			lastErr = fmt.Errorf("no usable output from any of: %s", strings.Join(attempted, ", "))
		}
		return hostProbeResult{Attempted: attempted, Err: lastErr}
	}

	attempted = append(attempted, "ip route show default")
	if out, err := m.sshExec(ctx, hostID, h, "ip route show default", probeCmdTimeout); err == nil {
		markDefaultInterface(ifaces, parseDefaultRouteIface(out))
	}
	return hostProbeResult{Method: method, Ifaces: ifaces, Attempted: attempted}
}

// remoteDiskFree reports total/free bytes at path on a remote host, trying
// POSIX `df` first (Linux/macOS) and falling back to `wmic` (Windows) if that
// fails — the same "try the common case, fall back for the rest" shape as
// remoteHostProbe, kept separate because it targets a specific path rather
// than enumerating interfaces.
func (m *Manager) remoteDiskFree(ctx context.Context, hostID int64, h *store.Host, path string) (totalBytes, freeBytes uint64, err error) {
	quoted := "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	out, dfErr := m.sshExec(ctx, hostID, h, "df -kP "+quoted, probeCmdTimeout)
	if dfErr == nil {
		if total, free, ok := parseDFOutput(out); ok {
			return total, free, nil
		}
	}

	drive := path
	if i := strings.IndexByte(path, ':'); i > 0 {
		drive = path[:i]
	}
	cmd := fmt.Sprintf(`wmic logicaldisk where "DeviceID='%s:'" get FreeSpace,Size /format:list`, drive)
	out, wErr := m.sshExec(ctx, hostID, h, cmd, probeCmdTimeout)
	if wErr == nil {
		if total, free, ok := parseWMICOutput(out); ok {
			return total, free, nil
		}
	}
	if dfErr != nil {
		return 0, 0, dfErr
	}
	return 0, 0, fmt.Errorf("could not parse free-space output from df or wmic")
}

var dfLineRe = regexp.MustCompile(`^\S+\s+(\d+)\s+\d+\s+(\d+)\s+\d+%`)

// parseDFOutput reads the second line of `df -kP` output: 1024-byte blocks
// total in the 2nd field, available in the 4th.
func parseDFOutput(out string) (totalBytes, freeBytes uint64, ok bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, false
	}
	m := dfLineRe.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if m == nil {
		return 0, 0, false
	}
	total, err1 := strconv.ParseUint(m[1], 10, 64)
	free, err2 := strconv.ParseUint(m[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return total * 1024, free * 1024, true
}

var wmicFieldRe = regexp.MustCompile(`(?i)^(FreeSpace|Size)=(\d+)\s*$`)

func parseWMICOutput(out string) (totalBytes, freeBytes uint64, ok bool) {
	var total, free uint64
	var haveTotal, haveFree bool
	for _, line := range strings.Split(out, "\n") {
		m := wmicFieldRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[2], 10, 64)
		if err != nil {
			continue
		}
		if strings.EqualFold(m[1], "Size") {
			total, haveTotal = v, true
		} else {
			free, haveFree = v, true
		}
	}
	return total, free, haveTotal && haveFree
}

func markDefaultInterface(ifaces []hostIface, name string) {
	if name == "" {
		return
	}
	for i := range ifaces {
		if ifaces[i].Name == name {
			ifaces[i].IsDefault = true
		}
	}
}

var defaultRouteRe = regexp.MustCompile(`\bdev\s+(\S+)`)

func parseDefaultRouteIface(out string) string {
	m := defaultRouteRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

// ---- ip -j addr (JSON) ----

type ipAddrInfoJSON struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

type ipLinkJSON struct {
	IfName   string           `json:"ifname"`
	MTU      int              `json:"mtu"`
	AddrInfo []ipAddrInfoJSON `json:"addr_info"`
}

func parseIPAddrJSON(out string) ([]hostIface, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}
	var links []ipLinkJSON
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		return nil, err
	}
	var ifaces []hostIface
	for _, l := range links {
		if l.IfName == "" {
			continue
		}
		hi := hostIface{Name: l.IfName, MTU: l.MTU}
		for _, a := range l.AddrInfo {
			if a.Family != "inet" && a.Family != "inet6" {
				continue
			}
			if a.Local == "" || a.PrefixLen <= 0 {
				continue
			}
			hi.Subnets = append(hi.Subnets, fmt.Sprintf("%s/%d", a.Local, a.PrefixLen))
		}
		if len(hi.Subnets) > 0 {
			ifaces = append(ifaces, hi)
		}
	}
	return ifaces, nil
}

// ---- ip addr (text) ----

var (
	ipLinkHeaderRe = regexp.MustCompile(`^\d+:\s+([^:@\s]+)(?:@\S+)?:.*\bmtu\s+(\d+)`)
	ipInetLineRe   = regexp.MustCompile(`^\s*inet6?\s+([0-9a-fA-F.:]+)/(\d+)`)
)

func parseIPAddrText(out string) ([]hostIface, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}
	var ifaces []hostIface
	var cur *hostIface
	for _, line := range strings.Split(out, "\n") {
		if m := ipLinkHeaderRe.FindStringSubmatch(line); m != nil {
			if cur != nil && len(cur.Subnets) > 0 {
				ifaces = append(ifaces, *cur)
			}
			mtu, _ := strconv.Atoi(m[2])
			cur = &hostIface{Name: m[1], MTU: mtu}
			continue
		}
		if cur == nil {
			continue
		}
		if m := ipInetLineRe.FindStringSubmatch(line); m != nil {
			prefix, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			cur.Subnets = append(cur.Subnets, fmt.Sprintf("%s/%d", m[1], prefix))
		}
	}
	if cur != nil && len(cur.Subnets) > 0 {
		ifaces = append(ifaces, *cur)
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no interfaces with an address found")
	}
	return ifaces, nil
}

// ---- ifconfig (text; Linux net-tools and BSD/macOS both) ----

var (
	ifconfigHeaderRe    = regexp.MustCompile(`^([^:\s]+):\s+flags=\d+<[^>]*>.*?\bmtu\s+(\d+)`)
	ifconfigInetLinuxRe = regexp.MustCompile(`^\s*inet\s+(?:addr:)?(\d+\.\d+\.\d+\.\d+)\s+netmask\s+(\S+)`)
)

func parseIfconfigText(out string) ([]hostIface, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}
	var ifaces []hostIface
	var cur *hostIface
	for _, line := range strings.Split(out, "\n") {
		if m := ifconfigHeaderRe.FindStringSubmatch(line); m != nil {
			if cur != nil && len(cur.Subnets) > 0 {
				ifaces = append(ifaces, *cur)
			}
			mtu, _ := strconv.Atoi(m[2])
			cur = &hostIface{Name: m[1], MTU: mtu}
			continue
		}
		if cur == nil {
			continue
		}
		if m := ifconfigInetLinuxRe.FindStringSubmatch(line); m != nil {
			prefix, ok := netmaskToPrefixLen(m[2])
			if !ok {
				continue
			}
			cur.Subnets = append(cur.Subnets, fmt.Sprintf("%s/%d", m[1], prefix))
		}
	}
	if cur != nil && len(cur.Subnets) > 0 {
		ifaces = append(ifaces, *cur)
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no interfaces with an address found")
	}
	return ifaces, nil
}

// netmaskToPrefixLen accepts either a dotted mask ("255.255.0.0", the Linux
// net-tools form) or a hex mask ("0xffffff00", macOS's form) and returns the
// CIDR prefix length.
func netmaskToPrefixLen(mask string) (int, bool) {
	if strings.HasPrefix(mask, "0x") {
		v, err := strconv.ParseUint(mask[2:], 16, 32)
		if err != nil {
			return 0, false
		}
		return countMaskBits(uint32(v)), true
	}
	parts := strings.Split(mask, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var v uint32
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return 0, false
		}
		v = v<<8 | uint32(n)
	}
	return countMaskBits(v), true
}

func countMaskBits(v uint32) int {
	n := 0
	for i := 31; i >= 0; i-- {
		if v&(1<<uint(i)) == 0 {
			break
		}
		n++
	}
	return n
}

// ---- ipconfig (Windows text) ----

var (
	ipconfigAdapterRe = regexp.MustCompile(`(?i)^\S.*adapter\s+(.+):\s*$`)
	ipconfigIPv4Re    = regexp.MustCompile(`(?i)IPv?4?\s*Address[.\s]*:\s*([0-9.]+)`)
	ipconfigMaskRe    = regexp.MustCompile(`(?i)Subnet\s+Mask[.\s]*:\s*([0-9.]+)`)
)

func parseIpconfigText(out string) ([]hostIface, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}
	var ifaces []hostIface
	adapter := "adapter"
	var pendingIP string
	for _, line := range strings.Split(out, "\n") {
		if m := ipconfigAdapterRe.FindStringSubmatch(line); m != nil {
			adapter = strings.TrimSpace(m[1])
			pendingIP = ""
			continue
		}
		if m := ipconfigIPv4Re.FindStringSubmatch(line); m != nil {
			pendingIP = m[1]
			continue
		}
		if m := ipconfigMaskRe.FindStringSubmatch(line); m != nil && pendingIP != "" {
			prefix, ok := netmaskToPrefixLen(m[1])
			if ok {
				ifaces = append(ifaces, hostIface{
					Name:    adapter,
					Subnets: []string{fmt.Sprintf("%s/%d", pendingIP, prefix)},
					// ipconfig does not report MTU.
				})
			}
			pendingIP = ""
		}
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no adapter with an IPv4 address found")
	}
	return ifaces, nil
}
