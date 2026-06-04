package docker

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// directDial is a probeDialer that connects straight over TCP (no SSH tunnel),
// used to point the fingerprinter at in-process fake servers.
func directDial(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

// serve starts a throwaway TCP server that runs handle on each connection and
// returns its address. It's torn down when the test ends.
func serve(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(c)
			}()
		}
	}()
	return ln.Addr().String()
}

func probe(t *testing.T, addr string) PortProbe {
	t.Helper()
	var pp PortProbe
	fingerprintPort(context.Background(), directDial, addr, &pp)
	return pp
}

func TestFingerprintSSH(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		_, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
		time.Sleep(200 * time.Millisecond)
	})
	pp := probe(t, addr)
	if !pp.Open || pp.Detected != "SSH" {
		t.Errorf("expected SSH, got %+v", pp)
	}
	if !strings.Contains(pp.Info, "OpenSSH") {
		t.Errorf("SSH banner should be captured: %q", pp.Info)
	}
}

func TestFingerprintSMTP(t *testing.T) {
	addr := serve(t, func(c net.Conn) { _, _ = c.Write([]byte("220 mail.example.com ESMTP Postfix\r\n")) })
	if pp := probe(t, addr); pp.Detected != "SMTP" {
		t.Errorf("expected SMTP, got %+v", pp)
	}
}

func TestFingerprintHTTP(t *testing.T) {
	// Server that waits for a request, then replies as HTTP (like a web server).
	addr := serve(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		_, _ = br.ReadString('\n') // consume the request line
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nServer: nginx/1.27\r\nContent-Length: 0\r\n\r\n"))
	})
	pp := probe(t, addr)
	if pp.Detected != "HTTP" {
		t.Fatalf("expected HTTP, got %+v", pp)
	}
	if !strings.Contains(pp.Info, "nginx") {
		t.Errorf("Server header should be captured: %q", pp.Info)
	}
}

func TestFingerprintRedis(t *testing.T) {
	// Silent until it receives PING, then replies +PONG (like Redis).
	addr := serve(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.ToUpper(line), "PING") {
				_, _ = c.Write([]byte("+PONG\r\n"))
				return
			}
			// Anything else (e.g. the HTTP probe) gets a Redis-style error.
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	})
	if pp := probe(t, addr); pp.Detected != "Redis" {
		t.Errorf("expected Redis, got %+v", pp)
	}
}

func TestFingerprintClosedPort(t *testing.T) {
	// Listen then immediately close so the port is dead but allocated.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	pp := probe(t, addr)
	if pp.Open {
		t.Errorf("closed port should not be open: %+v", pp)
	}
	if pp.Error == "" {
		t.Error("closed port should report an error")
	}
}

func TestWellKnownService(t *testing.T) {
	cases := map[uint16]string{22: "SSH", 80: "HTTP", 443: "HTTPS", 5432: "PostgreSQL", 6379: "Redis", 27017: "MongoDB"}
	for port, want := range cases {
		if got := wellKnownService(port, "tcp"); got != want {
			t.Errorf("wellKnownService(%d) = %q want %q", port, got, want)
		}
	}
	if got := wellKnownService(53, "udp"); got != "DNS" {
		t.Errorf("udp/53 should be DNS, got %q", got)
	}
	if got := wellKnownService(49152, "tcp"); got != "" {
		t.Errorf("unknown port should be empty, got %q", got)
	}
}

func TestHostFromAddr(t *testing.T) {
	if got := hostFromAddr("tcp://10.0.0.5:2376"); got != "10.0.0.5" {
		t.Errorf("hostFromAddr tcp = %q", got)
	}
	if got := hostFromAddr("example.com"); got != "example.com" {
		t.Errorf("hostFromAddr bare = %q", got)
	}
}
