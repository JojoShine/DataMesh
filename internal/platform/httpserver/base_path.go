package httpserver

import (
	"net/http"
	"strings"

	"datamesh/internal/platform/webpath"
)

func MountBasePath(base string, next http.Handler) (http.Handler, error) {
	base, err := webpath.Normalize(base)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return next, nil
	}
	stripped := http.StripPrefix(base, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The token-protected TLS bridge is an internal cluster endpoint. Nginx
		// must not expose it; existing Flink functions keep their fixed path.
		if r.URL.Path == "/internal/governance/stream-script" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == base {
			target := base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, base+"/") {
			http.NotFound(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	}), nil
}
