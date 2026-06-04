package docker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// PortProbe is the result of inspecting one published container port: the
// passive guess (from the well-known port number) plus the active fingerprint
// of whatever is actually listening.
type PortProbe struct {
	PrivatePort uint16 `json:"privatePort"`
	PublicPort  uint16 `json:"publicPort"`
	Type        string `json:"type"` // tcp | udp
	GuessByPort string `json:"guessByPort"`
	Open        bool   `json:"open"`
	Detected    string `json:"detected"`        // active fingerprint, "" if inconclusive
	Info        string `json:"info,omitempty"`  // banner / Server header / TLS subject
	TLS         bool   `json:"tls"`             // a TLS handshake succeeded
	Error       string `json:"error,omitempty"` // why the probe failed (closed/timeout)
}

// probeDialer opens a TCP connection to addr, however that host is reached
// (directly, or tunnelled through SSH).
type probeDialer func(ctx context.Context, addr string) (net.Conn, error)

// ProbeContainerPorts actively fingerprints every published TCP port of a
// container: it connects (locally, or tunnelled through SSH for ssh hosts) and
// classifies the listener by banner / HTTP / TLS / Redis handshake. UDP and
// unpublished ports get a passive guess only.
func (m *Manager) ProbeContainerPorts(ctx context.Context, hostID int64, containerID string) ([]PortProbe, error) {
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return nil, err
	}
	var ports []PortMapping
	for _, c := range containers {
		if c.ID == containerID || strings.HasPrefix(c.ID, containerID) {
			ports = c.Ports
			break
		}
	}

	dial, baseHost, err := m.probeTarget(ctx, hostID)
	if err != nil {
		return nil, err
	}

	// De-duplicate by published port (Docker lists v4 + v6 separately).
	seen := map[string]bool{}
	out := make([]PortProbe, 0, len(ports))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, statsConcurrency)

	for _, p := range ports {
		if p.PublicPort == 0 {
			continue // not published — nothing to reach from here
		}
		key := fmt.Sprintf("%d/%s", p.PublicPort, p.Type)
		if seen[key] {
			continue
		}
		seen[key] = true

		base := PortProbe{
			PrivatePort: p.PrivatePort,
			PublicPort:  p.PublicPort,
			Type:        p.Type,
			GuessByPort: wellKnownService(p.PrivatePort, p.Type),
		}
		if p.Type != "tcp" {
			// UDP can't be banner-grabbed reliably; report the guess only.
			mu.Lock()
			out = append(out, base)
			mu.Unlock()
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(pp PortProbe, port uint16) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(baseHost, strconv.Itoa(int(port)))
			fingerprintPort(ctx, dial, addr, &pp)
			mu.Lock()
			out = append(out, pp)
			mu.Unlock()
		}(base, p.PublicPort)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].PublicPort < out[j].PublicPort })
	return out, nil
}

// probeTarget returns a dialer plus the host address to use for the container's
// published ports, per host kind: local/tcp dial directly, ssh tunnels through
// the (cached) SSH connection to the remote host's loopback.
func (m *Manager) probeTarget(ctx context.Context, hostID int64) (probeDialer, string, error) {
	if hostID <= 0 {
		id, err := m.defaultHostID(ctx)
		if err != nil {
			return nil, "", err
		}
		hostID = id
	}
	h, err := m.store.HostByID(ctx, hostID)
	if err != nil {
		return nil, "", err
	}

	direct := func(ctx context.Context, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, "tcp", addr)
	}

	switch h.Kind {
	case "local", "":
		return direct, "127.0.0.1", nil
	case "tcp":
		return direct, hostFromAddr(h.Address), nil
	case "ssh":
		cli, err := m.sshClientFor(hostID, h)
		if err != nil {
			return nil, "", err
		}
		// Published ports live on the remote host; reach them via its loopback
		// through the SSH tunnel.
		return func(_ context.Context, addr string) (net.Conn, error) {
			return cli.Dial("tcp", addr)
		}, "127.0.0.1", nil
	default:
		return nil, "", fmt.Errorf("unknown host kind %q", h.Kind)
	}
}

// sshClientFor returns a cached SSH connection for the host, dialing one on
// first use. It is separate from the Docker client's own tunnel.
func (m *Manager) sshClientFor(hostID int64, h *store.Host) (*ssh.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.sshConns[hostID]; ok {
		return c, nil
	}
	c, err := dialSSH(h)
	if err != nil {
		return nil, err
	}
	m.sshConns[hostID] = c
	return c, nil
}

// hostFromAddr extracts the hostname from a daemon address like
// "tcp://host:2376" (falling back to the raw value).
func hostFromAddr(addr string) string {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "tcp://"), "https://")
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// fingerprintPort connects to addr and fills in the active-detection fields. It
// reads any immediate banner, then tries HTTP, a TLS handshake, and a Redis
// PING — enough to recognise the common services without a full scanner.
func fingerprintPort(ctx context.Context, dial probeDialer, addr string, pp *PortProbe) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := dial(cctx, addr)
	if err != nil {
		pp.Open = false
		pp.Error = trimErr(err)
		return
	}
	pp.Open = true

	// 1) Immediate banner (SSH, SMTP, FTP, IMAP/POP3, MySQL all greet first).
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	if n > 0 {
		banner := string(buf[:n])
		if svc, info := classifyBanner(banner); svc != "" {
			pp.Detected, pp.Info = svc, info
			_ = conn.Close()
			return
		}
		// 2) Server greeted but unrecognised — try an HTTP request on the same conn.
		if svc, info := tryHTTP(conn); svc != "" {
			pp.Detected, pp.Info = svc, info
			_ = conn.Close()
			return
		}
		pp.Info = printableSnippet(banner)
		_ = conn.Close()
		return
	}
	_ = conn.Close()

	// 3) Server waited for us: probe HTTP, then Redis, on fresh connections.
	if svc, info := httpProbe(cctx, dial, addr); svc != "" {
		pp.Detected, pp.Info = svc, info
		return
	}
	if svc, info, isTLS := tlsProbe(cctx, dial, addr); isTLS {
		pp.TLS = true
		pp.Detected, pp.Info = svc, info
		return
	}
	if redisPing(cctx, dial, addr) {
		pp.Detected = "Redis"
		return
	}
	pp.Detected = "" // open but unidentified
}

// classifyBanner recognises text protocols that greet on connect.
func classifyBanner(b string) (service, info string) {
	t := strings.TrimSpace(b)
	switch {
	case strings.HasPrefix(t, "SSH-"):
		return "SSH", firstLine(t)
	case strings.HasPrefix(t, "220") && containsFold(t, "ftp"):
		return "FTP", firstLine(t)
	case strings.HasPrefix(t, "220") && (containsFold(t, "smtp") || containsFold(t, "esmtp")):
		return "SMTP", firstLine(t)
	case strings.HasPrefix(t, "220"):
		return "SMTP/FTP", firstLine(t)
	case strings.HasPrefix(t, "+OK"):
		return "POP3", firstLine(t)
	case strings.HasPrefix(t, "* OK"):
		return "IMAP", firstLine(t)
	case containsFold(b, "mysql_native_password") || containsFold(b, "mariadb"):
		return "MySQL/MariaDB", mysqlVersion(b)
	case strings.HasPrefix(t, "HTTP/"):
		return "HTTP", firstLine(t)
	}
	return "", ""
}

// tryHTTP sends a minimal request on an already-open conn and detects HTTP.
func tryHTTP(conn net.Conn) (service, info string) {
	_ = conn.SetWriteDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := conn.Write([]byte("GET / HTTP/1.0\r\nHost: probe\r\n\r\n")); err != nil {
		return "", ""
	}
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	return parseHTTP(string(buf[:n]))
}

// httpProbe opens a fresh conn and detects HTTP.
func httpProbe(ctx context.Context, dial probeDialer, addr string) (service, info string) {
	conn, err := dial(ctx, addr)
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	return tryHTTP(conn)
}

func parseHTTP(resp string) (service, info string) {
	if !strings.HasPrefix(resp, "HTTP/") {
		return "", ""
	}
	info = firstLine(resp)
	for _, line := range strings.Split(resp, "\r\n") {
		if h, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(h), "Server") {
			info = "Server: " + strings.TrimSpace(v)
		}
	}
	return "HTTP", info
}

// tlsProbe attempts a TLS handshake; on success it reports the negotiated
// protocol and the certificate subject.
func tlsProbe(ctx context.Context, dial probeDialer, addr string) (service, info string, isTLS bool) {
	conn, err := dial(ctx, addr)
	if err != nil {
		return "", "", false
	}
	tconn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) // we inspect, don't trust
	_ = tconn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := tconn.Handshake(); err != nil {
		_ = conn.Close()
		return "", "", false
	}
	defer tconn.Close()
	st := tconn.ConnectionState()
	svc := "TLS"
	if st.NegotiatedProtocol == "h2" || st.NegotiatedProtocol == "http/1.1" {
		svc = "HTTPS"
	}
	subject := ""
	if len(st.PeerCertificates) > 0 {
		subject = st.PeerCertificates[0].Subject.CommonName
	}
	parts := []string{}
	if st.NegotiatedProtocol != "" {
		parts = append(parts, "alpn="+st.NegotiatedProtocol)
	}
	if subject != "" {
		parts = append(parts, "CN="+subject)
	}
	return svc, strings.Join(parts, " "), true
}

// redisPing checks for a Redis server (replies +PONG to PING).
func redisPing(ctx context.Context, dial probeDialer, addr string) bool {
	conn, err := dial(ctx, addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 32)
	n, _ := conn.Read(buf)
	return strings.HasPrefix(string(buf[:n]), "+PONG")
}

// wellKnownService is the passive guess from the (container-internal) port.
func wellKnownService(port uint16, typ string) string {
	if typ == "udp" {
		switch port {
		case 53:
			return "DNS"
		case 123:
			return "NTP"
		case 514:
			return "syslog"
		case 161:
			return "SNMP"
		}
		return ""
	}
	switch port {
	case 21:
		return "FTP"
	case 22:
		return "SSH"
	case 23:
		return "Telnet"
	case 25, 465, 587:
		return "SMTP"
	case 53:
		return "DNS"
	case 80, 8080, 8000, 8888:
		return "HTTP"
	case 110:
		return "POP3"
	case 143:
		return "IMAP"
	case 443, 8443:
		return "HTTPS"
	case 1433:
		return "MSSQL"
	case 3000:
		return "HTTP (Grafana/Node?)"
	case 3306:
		return "MySQL/MariaDB"
	case 5432:
		return "PostgreSQL"
	case 5672, 5671:
		return "AMQP (RabbitMQ)"
	case 6379:
		return "Redis"
	case 9000:
		return "HTTP (MinIO/PHP-FPM?)"
	case 9200:
		return "Elasticsearch"
	case 11211:
		return "Memcached"
	case 15672:
		return "RabbitMQ admin"
	case 27017:
		return "MongoDB"
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func containsFold(s, sub string) bool { return strings.Contains(strings.ToLower(s), sub) }

// printableSnippet returns a short, printable-only excerpt of an unknown banner.
func printableSnippet(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' {
			break
		}
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	return "banner: " + out
}

func mysqlVersion(b string) string {
	// The MySQL handshake carries a NUL-terminated ASCII version near the start.
	for i := 1; i+1 < len(b); i++ {
		if b[i] >= '0' && b[i] <= '9' {
			end := strings.IndexByte(b[i:], 0)
			if end > 0 && end < 24 {
				return "version " + b[i:i+end]
			}
			break
		}
	}
	return ""
}

func trimErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return "connection refused"
	}
	return firstLine(msg)
}
