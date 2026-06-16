package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Private-registry tag listing via the Docker Registry HTTP API v2. This only
// ever contacts a host the admin has explicitly configured as a registry
// (resolved through AuthForHost) — an arbitrary image ref can never make the
// server reach out to a host of the caller's choosing. It feeds editor/form
// autocomplete only, so it degrades to "no remote tags" on any error.

const maxRegistryRespBytes = 2 << 20 // 2 MiB cap on registry JSON responses

// isInternalIP reports whether an IP is in a range we must never reach from a
// registry-supplied URL (loopback, private, link-local, unspecified) — the
// classic SSRF targets, including cloud metadata (169.254.169.254).
func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// guardedDialer is the real SSRF control. It allows connections to allowHost —
// the registry the admin configured, which may legitimately be a private/local
// address — but for ANY other host (a token realm or a redirect target chosen by
// the registry) it resolves the address and refuses to connect if it lands on an
// internal IP. Because the check runs at actual dial time on the resolved IP, it
// covers IPv6, IPv4-mapped, decimal/hex/octal encodings and DNS-rebinding alike.
func guardedDialer(allowHost string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	base := &net.Dialer{Timeout: 5 * time.Second}
	allow := strings.ToLower(hostOnly(allowHost))
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host := hostOnly(addr)
		if strings.EqualFold(host, allow) {
			return base.DialContext(ctx, network, addr) // the configured registry
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		// Fail closed if ANY resolved IP is internal (a rebind attack could mix
		// internal + external answers), then dial the *verified* IPs directly so
		// there's no second resolution to race (no TOCTOU).
		for _, ip := range ips {
			if isInternalIP(ip) {
				return nil, fmt.Errorf("refusing to connect to internal address %s", ip)
			}
		}
		var firstErr error
		for _, ip := range ips {
			conn, err := base.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			firstErr = err
		}
		return nil, firstErr
	}
}

// newRegistryClient builds an HTTP client for talking to one registry. Short
// timeout (autocomplete stays snappy); it blocks internal targets via the dialer,
// refuses an https→http downgrade on redirect, and caps the redirect chain.
func newRegistryClient(allowHost string) *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext:         guardedDialer(allowHost),
			TLSHandshakeTimeout: 5 * time.Second,
			Proxy:               http.ProxyFromEnvironment,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing https→%s redirect", req.URL.Scheme)
			}
			return nil
		},
	}
}

// challengeParam extracts quoted key="value" pairs from a WWW-Authenticate header.
var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// hostOnly strips a :port from host[:port].
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// isLocalHost reports whether a registry host is loopback/localhost, in which
// case plain http (and a local token realm) is acceptable.
func isLocalHost(host string) bool {
	h := hostOnly(host)
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isInternalIPLiteral reports whether host is an IP literal in an internal
// range. It's a fast, clear-error pre-check for a token realm; the guarded
// dialer is the authoritative control (it also covers numeric encodings,
// hostnames and redirects). Pass a bracket-free host (url.URL.Hostname()).
func isInternalIPLiteral(host string) bool {
	ip := net.ParseIP(hostOnly(host))
	return ip != nil && isInternalIP(ip)
}

// registryScheme picks https for real registries, http for loopback ones.
func registryScheme(host string) string {
	if isLocalHost(host) {
		return "http"
	}
	return "https"
}

// repoPathForRef returns the repository path (host prefix and any tag/digest
// stripped) for a ref whose registry host is `host`, validated against the same
// pattern as Hub repos so a crafted ref can't escape the URL path.
func repoPathForRef(ref, host string) (string, bool) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	ref = strings.TrimPrefix(ref, strings.ToLower(host)+"/")
	if i := strings.LastIndexByte(ref, ':'); i >= 0 { // host already gone → any ':' is a tag
		ref = ref[:i]
	}
	if ref == "" || !dockerRepo.MatchString(ref) {
		return "", false
	}
	return ref, true
}

// parseAuthChallenge parses the quoted params of a WWW-Authenticate: Bearer header.
func parseAuthChallenge(h string) map[string]string {
	out := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(h, -1) {
		out[strings.ToLower(m[1])] = m[2]
	}
	return out
}

// registryV2Tags lists a repository's tags from a private registry's v2 API,
// performing the Bearer-token handshake when challenged. `auth` may carry
// credentials (anonymous if the username is empty).
func registryV2Tags(ctx context.Context, host, repo string, auth *store.RegistryAuth) ([]string, error) {
	tagsURL := registryScheme(host) + "://" + host + "/v2/" + repo + "/tags/list"
	client := newRegistryClient(host)

	get := func(bearer string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
		if err != nil {
			return nil, err
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else if auth != nil && auth.Username != "" {
			req.SetBasicAuth(auth.Username, auth.Password)
		}
		return client.Do(req)
	}

	resp, err := get("")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "bearer") {
			return []string{}, nil // a Basic challenge we couldn't satisfy → no remote tags
		}
		token, err := fetchRegistryToken(ctx, client, challenge, repo, host, auth)
		if err != nil {
			return nil, err
		}
		if resp, err = get(token); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil // unknown repo — no suggestions, not an error
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry %s: %s", host, resp.Status)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRegistryRespBytes)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Tags))
	for _, t := range body.Tags {
		if t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// fetchRegistryToken completes the Bearer-token handshake against the realm named
// in the registry's challenge, requesting only pull scope for the repo. The realm
// is constrained (https, no internal-IP target) to blunt SSRF via a hostile
// registry's challenge.
func fetchRegistryToken(ctx context.Context, client *http.Client, challenge, repo, regHost string, auth *store.RegistryAuth) (string, error) {
	params := parseAuthChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry auth: challenge has no realm")
	}
	u, err := url.Parse(realm)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("registry auth: invalid realm")
	}
	local := isLocalHost(regHost)
	if u.Scheme != "https" && !local {
		return "", fmt.Errorf("registry auth: refusing non-https token realm %q", u.Scheme)
	}
	// Fast pre-check on IP-literal realms (the guarded dialer is the authoritative
	// SSRF control and also covers hostnames, numeric encodings and redirects).
	if !local && isInternalIPLiteral(u.Hostname()) {
		return "", fmt.Errorf("registry auth: token realm points at an internal address")
	}

	q := u.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	q.Set("scope", "repository:"+repo+":pull")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if auth != nil && auth.Username != "" {
		req.SetBasicAuth(auth.Username, auth.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %s", resp.Status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRegistryRespBytes)).Decode(&tok); err != nil {
		return "", err
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	if tok.AccessToken != "" {
		return tok.AccessToken, nil
	}
	return "", fmt.Errorf("registry token: empty token")
}
