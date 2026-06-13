// Package mcp exposes a remote, RBAC-gated Model Context Protocol server over
// the application's existing HTTP listener.
//
// Security model (this is load-bearing — read before adding tools):
//
//   - OFF by default. The whole feature is mounted only when DC_MCP_ENABLED is
//     set; when off, none of these routes exist and the server returns a bare
//     404 with no hint the capability is present.
//   - Every request carries a bearer token, resolved to a LIVE user on every
//     single request (never cached), so revoking a token or a section in the
//     admin UI takes effect immediately.
//   - Tools are an explicit ALLOWLIST of narrow operations. This is never a
//     passthrough to the Docker API. There is deliberately no tool that reads
//     arbitrary files, exports/saves images, execs into containers, or browses
//     volume contents — those exfiltration vectors simply do not exist here.
//   - Secret-bearing fields (notably container environment variables) are
//     omitted from tool output.
//   - Each tool declares (section, write). Effective access is the token's
//     narrowing (an optional sections subset + a read-only flag) AND the live
//     user's RBAC via CheckAccess. A token can only ever shrink a user's rights.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// OAuth scopes advertised by both the protected-resource metadata and the
// authorization server, kept in one place so the two documents never disagree.
const (
	ScopeFull     = "mcp"
	ScopeReadOnly = "mcp:read"
)

// CheckAccessFunc is the host application's RBAC gate (api.Server.checkAccess).
// A nil error means the user may act on section with the given write intent.
type CheckAccessFunc func(ctx context.Context, u *store.User, section string, write bool) error

// ManagedProject is a Compose project the application manages and can deploy.
type ManagedProject struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Deployed bool   `json:"deployed"`
}

// Deps are the host-application dependencies the MCP server needs. The api
// package wires these from the running Server so there is no import cycle.
type Deps struct {
	Store       *store.Store
	Docker      *docker.Manager
	History     history.Store
	CheckAccess CheckAccessFunc
	Version     string

	// Managed-project control. These are provided by the host application
	// (api.Server) because deploy/down run the `docker compose` CLI against the
	// app's own project directories. Any may be nil, in which case the matching
	// tool reports the feature as unavailable.
	ListProjects  func(ctx context.Context) ([]ManagedProject, error)
	DeployProject func(ctx context.Context, id int64, profiles []string) (string, error)
	DownProject   func(ctx context.Context, id int64) (string, error)

	// ResourceURL is the canonical absolute URI of the /mcp endpoint
	// (e.g. https://host/mcp); empty when no public URL is configured.
	ResourceURL string
	// MetadataURL is the absolute URL of the protected-resource metadata doc.
	MetadataURL string
	// IssuerURL is the OAuth authorization-server issuer (the public base URL).
	IssuerURL string
	// SigningKey signs and verifies OAuth access tokens (HS256). Dedicated to
	// MCP — separate from the app session secret. Nil disables the OAuth/JWT
	// path (API-token bearer auth still works).
	SigningKey []byte
}

// handler bundles deps behind the SDK server and the bearer verifier.
type handler struct {
	deps Deps
}

// principal is the request-scoped identity resolved from a bearer token: the
// live user plus the token's narrowing constraints.
type principal struct {
	user   *store.User
	roOnly bool     // token forces read-only on top of the user's own flag
	scopes []string // when non-empty, restrict to this subset of sections
	ip     string
}

// principalKey stores the *principal inside TokenInfo.Extra.
const principalKey = "dc.principal"

// errEmptyContainer is returned when a control tool is called without a target.
var errEmptyContainer = errors.New("container_id is required")

// errProjectsUnavailable is returned when managed-project control is not wired.
var errProjectsUnavailable = errors.New("managed-project control is not available on this server")

// errInvalidProject is returned when a project tool is called without a valid id.
var errInvalidProject = errors.New("project_id must be a positive id from list_managed_projects")

// Handlers builds the HTTP handlers for the MCP feature: the bearer-gated /mcp
// transport and the protected-resource metadata document. The caller mounts
// these only when MCP is enabled.
func (d Deps) Handlers() (mcpHandler, metadataHandler http.Handler) {
	h := &handler{deps: d}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "docker-commander",
		Title:   "Docker Commander",
		Version: d.Version,
	}, nil)
	h.registerReadTools(srv)
	h.registerControlTools(srv)
	h.registerResources(srv)
	h.registerPrompts(srv)

	streamable := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		// Disable the SDK's loopback/DNS-rebinding guard: it 403s when the server
		// listens on 127.0.0.1 but the Host header is a public domain — exactly
		// the reverse-proxy setup this feature targets. The guard defends against
		// browser DNS-rebinding with ambient auth; /mcp has none (bearer only, no
		// cookies), so disabling it gains an attacker nothing.
		&mcpsdk.StreamableHTTPOptions{DisableLocalhostProtection: true})

	gated := auth.RequireBearerToken(h.verifyToken, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: d.MetadataURL,
	})(streamable)

	meta := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               d.ResourceURL,
		AuthorizationServers:   issuers(d.IssuerURL),
		ScopesSupported:        []string{ScopeFull, ScopeReadOnly},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Docker Commander",
	})
	return gated, meta
}

func issuers(issuer string) []string {
	if issuer == "" {
		return nil
	}
	return []string{issuer}
}

// verifyToken implements auth.TokenVerifier. It hashes the presented secret,
// looks up a live, non-revoked, non-expired API token, loads its owning user,
// and packs a request-scoped principal into the returned TokenInfo. Failures
// collapse to ErrInvalidToken so the caller cannot distinguish "unknown token"
// from "revoked" from "expired".
func (h *handler) verifyToken(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
	// OAuth access-token (JWT) path, tried first when OAuth is configured. A JWS
	// has exactly two dots; only accept it if it fully verifies (signature, alg,
	// expiry, audience), otherwise fall through to the opaque-token path.
	if len(h.deps.SigningKey) > 0 && h.deps.ResourceURL != "" && strings.Count(token, ".") == 2 {
		if uid, ro, exp, err := parseAccessToken(h.deps.SigningKey, h.deps.ResourceURL, token); err == nil {
			u, uerr := h.deps.Store.UserByID(ctx, uid)
			if uerr != nil {
				return nil, auth.ErrInvalidToken
			}
			return h.tokenInfo(u, &principal{user: u, roOnly: ro, ip: clientIP(req)}, exp), nil
		}
	}

	// Opaque API-token path.
	sum := sha256.Sum256([]byte(token))
	tok, err := h.deps.Store.APITokenByHash(ctx, hex.EncodeToString(sum[:]))
	if err != nil || tok.Expired() {
		return nil, auth.ErrInvalidToken
	}
	u, err := h.deps.Store.UserByID(ctx, tok.UserID)
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	_ = h.deps.Store.TouchAPIToken(ctx, tok.ID) // best-effort last-used stamp

	// The SDK rejects a zero Expiration; for never-expiring tokens, hand it a
	// far-future instant. Real expiry is still enforced via tok.Expired() above.
	exp := tok.ExpiresAt
	if exp.IsZero() {
		exp = time.Now().Add(100 * 365 * 24 * time.Hour)
	}
	return h.tokenInfo(u, &principal{user: u, roOnly: tok.ReadOnly, scopes: tok.Sections, ip: clientIP(req)}, exp), nil
}

// tokenInfo packs a resolved principal into the SDK's TokenInfo envelope.
func (h *handler) tokenInfo(u *store.User, p *principal, exp time.Time) *auth.TokenInfo {
	return &auth.TokenInfo{
		UserID:     strconv.FormatInt(u.ID, 10),
		Expiration: exp,
		Extra:      map[string]any{principalKey: p},
	}
}

// narrowed applies the token's own constraints — a token can only ever reduce
// its owner's rights. It runs BEFORE the user-level RBAC check, so a read-only
// or section-scoped token is rejected even for an admin owner.
func (p *principal) narrowed(section string, write bool) error {
	if write && p.roOnly {
		return errors.New("this token is read-only")
	}
	if len(p.scopes) > 0 && !contains(p.scopes, section) {
		return errors.New("this token is not scoped for the " + section + " section")
	}
	return nil
}

// principalFromExtra extracts the request-scoped principal placed by the bearer
// verifier. The streamable transport delivers the per-request TokenInfo via
// RequestExtra (NOT via the handler's context), so we read it from there; this
// also means the principal is re-resolved on every HTTP request, so a revoked
// token or section stops working immediately. It is shared by tool and resource
// handlers (both carry a RequestExtra).
func principalFromExtra(re *mcpsdk.RequestExtra) *principal {
	if re == nil || re.TokenInfo == nil {
		return nil
	}
	p, _ := re.TokenInfo.Extra[principalKey].(*principal)
	return p
}

// authorizeExtra is the shared gate. It applies the token's narrowing first (a
// token can only reduce rights) and then the live user RBAC. It returns the
// principal so write tools can audit under the acting user.
func (h *handler) authorizeExtra(ctx context.Context, re *mcpsdk.RequestExtra, section string, write bool) (*principal, error) {
	p := principalFromExtra(re)
	if p == nil {
		return nil, errors.New("unauthenticated")
	}
	if err := p.narrowed(section, write); err != nil {
		return nil, err
	}
	if err := h.deps.CheckAccess(ctx, p.user, section, write); err != nil {
		return nil, err
	}
	return p, nil
}

// authorize gates a tool call. Thin wrapper over authorizeExtra.
func (h *handler) authorize(ctx context.Context, req *mcpsdk.CallToolRequest, section string, write bool) (*principal, error) {
	return h.authorizeExtra(ctx, req.Extra, section, write)
}

// audit records a mutating tool call under the acting user. Best-effort.
func (h *handler) audit(p *principal, action, target, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.deps.Store.Audit(ctx, store.AuditEntry{
		UserID: p.user.ID, Username: p.user.Username,
		Action: action, Target: target, Detail: detail, IP: p.ip,
	})
}

// Note on errors: a typed tool handler that returns a non-nil Go error is
// turned by the SDK into a tool-level result with IsError set and the message
// as content (not a protocol error), so the model sees the reason and can
// adapt. Denials and failures therefore just `return nil, zero, err`.

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	if r == nil {
		return "mcp"
	}
	return r.RemoteAddr
}
