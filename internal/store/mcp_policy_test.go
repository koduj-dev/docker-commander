package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The policy's job is to make a forgotten credential stop working on its own.
// Everything below is about the ways that job can be quietly undone: a zero that
// means the wrong thing, a contradiction that disables the rule, or a corrupt
// row that fails open.

var policyEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestDefaultPolicyExpiresTokens(t *testing.T) {
	p := DefaultMCPTokenPolicy()
	if p.DefaultDays != 30 {
		t.Fatalf("default lifetime is %d days, want 30", p.DefaultDays)
	}
	if p.AllowUnlimited {
		t.Fatal("a fresh install allows never-expiring tokens by default")
	}
	if p.MaxDays <= 0 {
		t.Fatal("a fresh install has no ceiling, so 'no unlimited tokens' can be sidestepped by asking for 99999 days")
	}
}

func TestUnspecifiedLifetimeUsesTheDefaultNotForever(t *testing.T) {
	p := MCPTokenPolicy{DefaultDays: 30, MaxDays: 365}
	// Zero days is "I did not choose". Reading that as "never" is the single most
	// damaging way this could be wired, so it is asserted directly.
	exp, err := p.ResolveExpiry(0, false, policyEpoch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exp.IsZero() {
		t.Fatal("an unspecified lifetime produced a token that never expires")
	}
	if want := policyEpoch.Add(30 * 24 * time.Hour); !exp.Equal(want) {
		t.Fatalf("expiry %v, want the policy default %v", exp, want)
	}
}

func TestNeverIsRefusedUnlessAllowed(t *testing.T) {
	p := MCPTokenPolicy{DefaultDays: 30, MaxDays: 365}
	if _, err := p.ResolveExpiry(0, true, policyEpoch); !errors.Is(err, ErrTokenNeverForbidden) {
		t.Fatalf("a never-expiring token was minted against policy: %v", err)
	}

	p.AllowUnlimited = true
	exp, err := p.ResolveExpiry(0, true, policyEpoch)
	if err != nil {
		t.Fatalf("never refused even though the admin allowed it: %v", err)
	}
	if !exp.IsZero() {
		t.Fatalf("expected no expiry, got %v", exp)
	}
}

func TestCeilingIsEnforcedAndNamesItself(t *testing.T) {
	p := MCPTokenPolicy{DefaultDays: 30, MaxDays: 365}
	_, err := p.ResolveExpiry(99999, false, policyEpoch)
	if err == nil {
		t.Fatal("a 99999-day token was accepted, which is 'never expires' spelled differently")
	}
	var lim *TokenLifetimeError
	if !errors.As(err, &lim) || lim.MaxDays != 365 {
		t.Fatalf("refusal does not name the actual limit: %v", err)
	}
	// The boundary itself must be allowed — a ceiling nobody can reach is a
	// different, lower ceiling.
	if _, err := p.ResolveExpiry(365, false, policyEpoch); err != nil {
		t.Fatalf("the ceiling value itself was refused: %v", err)
	}
}

func TestNormalizeRepairsContradictions(t *testing.T) {
	// No ceiling while never-expiring tokens are forbidden is the combination
	// that makes the rule meaningless.
	got := normalizeMCPTokenPolicy(MCPTokenPolicy{DefaultDays: 30, MaxDays: 0, AllowUnlimited: false})
	if got.MaxDays <= 0 {
		t.Fatal("forbidding 'never' while leaving no ceiling: the policy can be defeated by a large number")
	}
	// A default beyond the ceiling would hand out lifetimes nobody is allowed to
	// ask for.
	got = normalizeMCPTokenPolicy(MCPTokenPolicy{DefaultDays: 900, MaxDays: 90})
	if got.DefaultDays != 90 {
		t.Fatalf("default %d exceeds the ceiling %d", got.DefaultDays, got.MaxDays)
	}
	// Nonsense input must not become "no policy".
	got = normalizeMCPTokenPolicy(MCPTokenPolicy{DefaultDays: -5, MaxDays: -1})
	if got.DefaultDays <= 0 || got.MaxDays <= 0 {
		t.Fatalf("negative input produced an unenforceable policy: %+v", got)
	}
}

func TestStoredPolicyRoundTripsAndFailsSafe(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	// Unset: the safe default, not a zero value.
	p, err := st.MCPTokenPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.AllowUnlimited || p.DefaultDays != 30 {
		t.Fatalf("an install that never configured a policy is not protected: %+v", p)
	}

	if err := st.SetMCPTokenPolicy(ctx, MCPTokenPolicy{DefaultDays: 7, MaxDays: 90, AllowUnlimited: true}); err != nil {
		t.Fatal(err)
	}
	if p, err = st.MCPTokenPolicy(ctx); err != nil || p.DefaultDays != 7 || p.MaxDays != 90 || !p.AllowUnlimited {
		t.Fatalf("policy did not round-trip: %+v (%v)", p, err)
	}

	// A corrupt row must fail SAFE. Falling through to a zero value would read as
	// "no ceiling, unlimited allowed" — the one outcome a broken setting must
	// never produce.
	if err := st.SetSetting(ctx, mcpTokenPolicyKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	p, err = st.MCPTokenPolicy(ctx)
	if err != nil {
		t.Fatalf("corrupt policy returned an error instead of a safe default: %v", err)
	}
	if p.AllowUnlimited || p.MaxDays <= 0 || p.DefaultDays <= 0 {
		t.Fatalf("corrupt policy failed open: %+v", p)
	}
}

// A lifetime long enough to overflow the arithmetic must be refused, not
// silently turned into something else.
//
// time.Duration is int64 nanoseconds, so days × 24h wraps above ~106,751 days.
// Before this was bounded, 200,000 days produced an expiry in 1989 — a token
// dead the moment it was issued — and larger values wrapped round to arbitrary
// dates bearing no relation to the request. It fails safe, but it answers the
// wrong question without saying so, which is worse than refusing.
func TestAbsurdLifetimeIsRefusedNotWrapped(t *testing.T) {
	// No ceiling: the only configuration in which a huge request gets this far.
	p := MCPTokenPolicy{DefaultDays: 30, MaxDays: 0, AllowUnlimited: true}

	for _, days := range []int{200_000, 1 << 40} {
		exp, err := p.ResolveExpiry(days, false, policyEpoch)
		if err == nil {
			t.Errorf("%d days accepted, producing %v", days, exp)
			continue
		}
		var lim *TokenLifetimeError
		if !errors.As(err, &lim) {
			t.Errorf("%d days: want a lifetime error, got %v", days, err)
		}
	}

	// A long-but-sane lifetime still works, and lands in the future — the
	// boundary must not be so tight that it breaks legitimate use.
	exp, err := p.ResolveExpiry(3650, false, policyEpoch)
	if err != nil {
		t.Fatalf("a 10-year token was refused: %v", err)
	}
	if !exp.After(policyEpoch) {
		t.Fatalf("expiry %v is not in the future", exp)
	}
}
