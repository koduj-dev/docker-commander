package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// ctxAs builds a context carrying a user's claims, as the session middleware
// would after verifying a token.
func ctxAs(uid int64, role string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{UserID: uid, Role: role})
}

// PENTEST: targeting a NON-local host requires the "hosts" permission, so a user
// with only "projects" can't deploy to / tear down a host they can't see.
func TestRequireHostAccess(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	adminID, _ := st.CreateUser(ctx, &store.User{Username: "admin", Role: "admin"})
	devID, _ := st.CreateUser(ctx, &store.User{Username: "dev", Role: "user", Sections: []string{"projects"}})
	opsID, _ := st.CreateUser(ctx, &store.User{Username: "ops", Role: "user", Sections: []string{"projects", "hosts"}})

	srv := &Server{cfg: config.Config{}, store: st}

	cases := []struct {
		name     string
		uid      int64
		role     string
		hostID   int64
		wantDeny bool
	}{
		{"local host, any user", devID, "user", 0, false},
		{"remote host, projects-only user denied", devID, "user", 5, true},
		{"remote host, hosts-section user allowed", opsID, "user", 5, false},
		{"remote host, admin allowed", adminID, "admin", 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/projects/1/deploy", nil).WithContext(ctxAs(c.uid, c.role))
			err := srv.requireHostAccess(r, c.hostID)
			if c.wantDeny && err == nil {
				t.Error("expected the remote host to be denied")
			}
			if !c.wantDeny && err != nil {
				t.Errorf("expected access, got %v", err)
			}
		})
	}
}
