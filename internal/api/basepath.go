package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/config"
)

// forwardedPrefixHeader names the path prefix a reverse proxy publishes this
// server under (nginx/Traefik convention).
const forwardedPrefixHeader = "X-Forwarded-Prefix"

type pathPrefixKey struct{}

// pathPrefix resolves the URL path prefix of a request and strips it from the
// path, so every route below sees the same paths whether the UI is published
// at the root or under e.g. /dockercmd.
//
// The prefix comes from X-Forwarded-Prefix (unless DC_FORWARDED_PREFIX=0),
// otherwise from DC_BASE_PATH. A header value that isn't a plain path is
// ignored, not trusted halfway. The request may carry the prefix (a proxy that
// forwards the path as-is) or not (one that strips it); both route the same.
//
// Believing the header from any peer is deliberate: what it changes is the
// prefix written into this response's links and cookie path, and a browser
// can't be made to send it cross-origin, so a client can only misdirect itself.
func (s *Server) pathPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := s.cfg.BasePath
		if s.cfg.ForwardedPrefix {
			if h := r.Header.Get(forwardedPrefixHeader); h != "" {
				if p, err := config.NormalizePathPrefix(h); err == nil {
					prefix = p
				}
			}
		}
		if prefix != "" {
			r = stripPathPrefix(r, prefix)
			r = r.WithContext(context.WithValue(r.Context(), pathPrefixKey{}, prefix))
		}
		next.ServeHTTP(w, r)
	})
}

// stripPathPrefix removes prefix from the request path when the path starts
// with it as whole segments ("/dockercmd" matches "/dockercmd/x", never
// "/dockercmdx"). The prefix holds only unreserved characters, so its raw and
// decoded spellings are the same and RawPath is cut alike.
func stripPathPrefix(r *http.Request, prefix string) *http.Request {
	rest, ok := cutPathPrefix(r.URL.Path, prefix)
	if !ok {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = rest
	if r.URL.RawPath != "" {
		if raw, ok := cutPathPrefix(r.URL.RawPath, prefix); ok {
			r2.URL.RawPath = raw
		} else {
			r2.URL.RawPath = ""
		}
	}
	return r2
}

func cutPathPrefix(p, prefix string) (string, bool) {
	if p == prefix {
		return "/", true
	}
	if strings.HasPrefix(p, prefix+"/") {
		return p[len(prefix):], true
	}
	return p, false
}

// requestPrefix is the URL path prefix the request was made under ("" for the
// root), as resolved by pathPrefix.
func requestPrefix(r *http.Request) string {
	p, _ := r.Context().Value(pathPrefixKey{}).(string)
	return p
}
