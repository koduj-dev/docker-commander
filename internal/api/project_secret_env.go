package api

import (
	"context"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// projectSecretEnvs resolves a project's secrets once and returns both views
// every deploy/preview/validate/resolve call site needs: real ("NAME=value",
// decrypted — for an actual deploy, which must receive the real value) and
// masked ("NAME=secret:<fingerprint>" — for every preview/validate/resolve
// call, so ${NAME} compose interpolation never sees the real value there in
// the first place), plus the bare name set (used to redact the LIVE side of
// a diff via maskLiveSecrets, since `docker inspect` shows whatever value a
// running container actually started with, in the clear).
func (s *Server) projectSecretEnvs(ctx context.Context, projectID int64) (real, masked []string, names map[string]struct{}, err error) {
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
	names = make(map[string]struct{}, len(secrets))
	real = make([]string, 0, len(secrets))
	masked = make([]string, 0, len(secrets))
	for k, v := range secrets {
		real = append(real, k+"="+v)
		masked = append(masked, k+"="+s.store.MaskSecretValue(v))
		names[k] = struct{}{}
	}
	return real, masked, names, nil
}

// maskLiveSecrets rewrites, in place, any Env key in names to its masked
// placeholder — redacts a running container's REAL env (LiveServiceSpec
// reads it straight from `docker inspect`, which necessarily shows whatever
// value the container actually started with).
func (s *Server) maskLiveSecrets(specs []docker.ServiceSpec, names map[string]struct{}) {
	if len(names) == 0 {
		return
	}
	for i := range specs {
		for k, v := range specs[i].Env {
			if _, ok := names[k]; ok {
				specs[i].Env[k] = s.store.MaskSecretValue(v)
			}
		}
	}
}
