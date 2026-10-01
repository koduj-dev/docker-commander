package store

import (
	"context"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

// With two entries for one registry, AuthForHost and AllRegistryAuths agree that
// the oldest one comes first, so the Images page and deploys use the same login.
func TestDuplicateRegistriesResolveToTheOldest(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cph, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(cph)
	ctx := context.Background()
	for _, u := range []string{"first", "second"} {
		if _, err := st.CreateRegistry(ctx, u, "ghcr.io", u, "pw-"+u); err != nil {
			t.Fatal(err)
		}
	}
	a, err := st.AuthForHost(ctx, "ghcr.io")
	if err != nil || a.Username != "first" {
		t.Fatalf("AuthForHost = %+v, %v; want the oldest entry", a, err)
	}
	all, err := st.AllRegistryAuths(ctx)
	if err != nil || len(all) != 2 || all[0].Username != "first" {
		t.Fatalf("AllRegistryAuths = %+v, %v; want the oldest first", all, err)
	}
}
