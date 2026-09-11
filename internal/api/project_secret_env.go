package api

import (
	"context"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// projectSecretEnvs resolves a project's secrets once and returns every view
// a deploy/preview/validate/resolve call site needs: real ("NAME=value",
// decrypted — for an actual deploy, which must receive the real value) and
// masked ("NAME=secret:<fingerprint>" — for every preview/validate/resolve
// call, so ${NAME} compose interpolation never sees the real value there in
// the first place), plus the bare plaintext values (used to redact the LIVE
// side of a diff via maskLiveSecrets, since `docker inspect` shows whatever
// value a running container actually started with, in the clear).
//
// values, not names, is what maskLiveSecrets matches against: a compose
// service is free to interpolate a secret into a differently-named target
// (`DATABASE_PASSWORD: ${DB_PASSWORD}`), so the live container's env key is
// "DATABASE_PASSWORD" — comparing against the stored secret's NAME would
// silently miss it. Matching by value catches that regardless of the target
// key's name.
func (s *Server) projectSecretEnvs(ctx context.Context, projectID int64) (real, masked, values []string, err error) {
	// ListProjectSecrets needs no cipher (names only) — check it first so a
	// project with no secrets at all never depends on the cipher being
	// configured, and only a project that actually HAS secrets can fail
	// closed on a cipher problem.
	list, err := s.store.ListProjectSecrets(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(list) == 0 {
		return nil, nil, nil, nil
	}
	secrets, err := s.store.ResolveProjectSecretEnv(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	real = make([]string, 0, len(secrets))
	masked = make([]string, 0, len(secrets))
	values = make([]string, 0, len(secrets))
	for k, v := range secrets {
		real = append(real, k+"="+v)
		masked = append(masked, k+"="+s.store.MaskSecretValue(v))
		values = append(values, v)
	}
	return real, masked, values, nil
}

// maskLiveSecrets rewrites, in place, any Env value in specs that contains
// one of the project's current secret values — redacting a running
// container's REAL env (LiveServiceSpec reads it straight from `docker
// inspect`, which necessarily shows whatever value the container actually
// started with) regardless of which key it landed under, and even when the
// secret was only part of a larger interpolated string. The whole field is
// replaced (not just the matched substring) so the placeholder's fingerprint
// covers the field's full content, keeping same-vs-changed comparisons
// meaningful.
//
// Known limitation: this can only redact a value that is STILL one of the
// project's current secrets. A container already running with a value from
// a secret that has since been deleted or changed keeps showing that stale
// value here — Docker Commander no longer has anything to match it against.
// That's not a new exposure: anyone with API/exec access to that host
// already sees the container's real env directly; this only narrows what
// Docker Commander's own preview/diff surface additionally exposes.
func (s *Server) maskLiveSecrets(specs []docker.ServiceSpec, values []string) {
	if len(values) == 0 {
		return
	}
	for i := range specs {
		for k, v := range specs[i].Env {
			for _, sv := range values {
				if sv != "" && strings.Contains(v, sv) {
					specs[i].Env[k] = s.store.MaskSecretValue(v)
					break
				}
			}
		}
	}
}
