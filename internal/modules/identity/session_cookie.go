package identity

import (
	"datamesh/internal/platform/webpath"
	"fmt"
)

func (h *Handler) ConfigureSessionCookiePath(base string) error {
	normalized, err := webpath.Normalize(base)
	if err != nil {
		return err
	}
	if normalized == "" {
		normalized = "/"
	}
	h.cookiePath = normalized
	return nil
}

// ConfigureSessionCookie isolates sessions when instances share a hostname.
// Ports do not isolate cookies, so the experience instance uses a distinct name.
func (h *Handler) ConfigureSessionCookie(name string) error {
	switch name {
	case "", CookieName:
		h.cookieName = CookieName
	case "datamesh_experience_session":
		h.cookieName = name
	default:
		return fmt.Errorf("unsupported session cookie namespace")
	}
	return nil
}
