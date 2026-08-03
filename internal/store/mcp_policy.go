package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// How long an MCP bearer token is allowed to live.
//
// A token that never expires is a credential nobody ever has to think about
// again — which is exactly the problem. It outlives the laptop it was pasted
// into, the contractor it was minted for, and the incident it was involved in.
// Revocation exists, but revocation requires somebody to remember; an expiry
// date is the only control here that works when nobody is paying attention.
//
// So the default is 30 days, and never-expiring tokens are off unless an admin
// turns them back on. This governs what may be MINTED — it deliberately does not
// touch tokens that already exist, because silently expiring credentials that
// are in use would break running setups to enforce a policy chosen after they
// were created. Existing forever-tokens are visible on the MCP admin page and
// can be revoked there.
type MCPTokenPolicy struct {
	// DefaultDays is the lifetime applied when the request doesn't name one.
	DefaultDays int `json:"defaultDays"`
	// MaxDays is the longest lifetime a user may choose. 0 means no ceiling.
	//
	// This exists so that "no unlimited tokens" means something. Without a
	// ceiling the rule is a formality: anyone told they cannot have a token that
	// never expires can simply ask for one lasting a hundred years, and the
	// policy has achieved nothing but a longer number.
	MaxDays int `json:"maxDays"`
	// AllowUnlimited permits tokens with no expiry at all.
	AllowUnlimited bool `json:"allowUnlimited"`
}

const (
	mcpTokenPolicyKey = "mcp_token_policy"

	defaultMCPTokenDays = 30
	// A year is the ceiling when unlimited tokens are off: long enough for a
	// genuine long-lived integration, short enough that a forgotten credential
	// eventually stops working on its own.
	defaultMCPTokenMaxDays = 365
)

// DefaultMCPTokenPolicy is what a fresh install gets, and what a stored policy
// falls back to field by field when it is missing or nonsensical.
func DefaultMCPTokenPolicy() MCPTokenPolicy {
	return MCPTokenPolicy{DefaultDays: defaultMCPTokenDays, MaxDays: defaultMCPTokenMaxDays}
}

// MCPTokenPolicy reads the policy, falling back to the default when unset.
func (s *Store) MCPTokenPolicy(ctx context.Context) (MCPTokenPolicy, error) {
	p := DefaultMCPTokenPolicy()
	raw, err := s.Setting(ctx, mcpTokenPolicyKey)
	if err != nil || raw == "" {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// A corrupt policy row must not become "no policy". Falling back to the
		// default keeps the safe behaviour; falling through to a zero value would
		// quietly mean unlimited.
		return DefaultMCPTokenPolicy(), nil
	}
	return normalizeMCPTokenPolicy(p), nil
}

// SetMCPTokenPolicy persists the policy after normalising it.
func (s *Store) SetMCPTokenPolicy(ctx context.Context, p MCPTokenPolicy) error {
	b, err := json.Marshal(normalizeMCPTokenPolicy(p))
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, mcpTokenPolicyKey, string(b))
}

// normalizeMCPTokenPolicy repairs a policy into one that can be enforced
// coherently, so no caller has to handle a contradictory combination.
func normalizeMCPTokenPolicy(p MCPTokenPolicy) MCPTokenPolicy {
	if p.DefaultDays <= 0 {
		p.DefaultDays = defaultMCPTokenDays
	}
	if p.MaxDays < 0 {
		p.MaxDays = 0
	}
	// A ceiling of "none" while unlimited tokens are forbidden is the
	// contradiction that would make the rule meaningless, so it gets one.
	if !p.AllowUnlimited && p.MaxDays == 0 {
		p.MaxDays = defaultMCPTokenMaxDays
	}
	// A default longer than the ceiling would hand every token that didn't ask
	// for a lifetime one the user isn't allowed to request.
	if p.MaxDays > 0 && p.DefaultDays > p.MaxDays {
		p.DefaultDays = p.MaxDays
	}
	return p
}

// ErrTokenNeverForbidden is returned when a never-expiring token is asked for
// and the policy does not allow one.
var ErrTokenNeverForbidden = errors.New("never-expiring MCP tokens are not allowed by the administrator")

// TokenLifetimeError reports a requested lifetime beyond the policy ceiling. A
// typed error so the message can name the actual limit — a bare "too long"
// leaves the user guessing what to type instead.
type TokenLifetimeError struct{ MaxDays int }

func (e *TokenLifetimeError) Error() string {
	return fmt.Sprintf("MCP tokens may last at most %d days", e.MaxDays)
}

// ResolveExpiry turns a request into a concrete expiry time.
//
// "Never" is a separate flag rather than days==0 on purpose. Overloading zero
// would make the two very different intents — "I did not choose" and "I want
// this to live forever" — indistinguishable on the wire, and the safe reading of
// silence has to be the policy default, not immortality.
//
// The whole decision lives here so the handler cannot enforce it slightly
// differently from anything added later. now is passed in so the tests are not
// timing-dependent.
func (p MCPTokenPolicy) ResolveExpiry(requestedDays int, never bool, now time.Time) (time.Time, error) {
	if never {
		if !p.AllowUnlimited {
			return time.Time{}, ErrTokenNeverForbidden
		}
		return time.Time{}, nil
	}
	days := requestedDays
	if days <= 0 {
		days = p.DefaultDays
	}
	if p.MaxDays > 0 && days > p.MaxDays {
		return time.Time{}, &TokenLifetimeError{MaxDays: p.MaxDays}
	}
	return now.Add(time.Duration(days) * 24 * time.Hour), nil
}
