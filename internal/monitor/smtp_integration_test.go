package monitor

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// startMailhog runs a throwaway MailHog SMTP sink and returns its host:port.
// Skipped when Docker/the image is unavailable.
func startMailhog(t *testing.T) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::1025", "mailhog/mailhog:latest").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start mailhog: %v (%s)", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	var addr string
	for i := 0; i < 20; i++ {
		if portOut, err := exec.Command("docker", "port", id, "1025/tcp").Output(); err == nil {
			addr = strings.TrimSpace(strings.SplitN(string(portOut), "\n", 2)[0])
			if addr != "" {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if addr == "" {
		t.Skip("mailhog port never became available")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Skipf("bad mapped addr %q: %v", addr, err)
	}
	port, _ := strconv.Atoi(portStr)

	// Wait for the SMTP port to accept connections.
	for i := 0; i < 40; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return host, port
}

func TestSendMailIntegration(t *testing.T) {
	host, port := startMailhog(t)
	cfg := store.SMTPConfig{
		Host: host,
		Port: port,
		From: "alerts@docker-commander.test",
		To:   "ops@docker-commander.test, oncall@docker-commander.test",
	}
	if err := SendMail(cfg, "Test subject", "Hello from the test.\n"); err != nil {
		t.Fatalf("SendMail to mailhog failed: %v", err)
	}
}
