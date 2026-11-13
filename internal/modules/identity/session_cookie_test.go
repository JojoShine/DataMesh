package identity

import "testing"

func TestSessionCookiePath(t *testing.T) {
	h := NewHandler(nil, "https://tbtparent.me")
	for input, want := range map[string]string{"": "/", "/": "/", "/datamesh": "/datamesh", "/datamesh/": "/datamesh"} {
		if err := h.ConfigureSessionCookiePath(input); err != nil || h.cookiePath != want {
			t.Fatalf("unexpected cookie scope for %q", input)
		}
	}
	for _, input := range []string{"/../other", "/x; Path=/", "/x\r\nX-Injected: true"} {
		if h.ConfigureSessionCookiePath(input) == nil || h.cookiePath != "/datamesh" {
			t.Fatal("invalid cookie scope accepted or changed current scope")
		}
	}
}

func TestExperienceSessionCookieIsolation(t *testing.T) {
	main := NewHandler(nil, "https://localhost:18443")
	experience := NewHandler(nil, "https://localhost:19443")
	if err := experience.ConfigureSessionCookie("datamesh_experience_session"); err != nil {
		t.Fatal(err)
	}
	if main.cookieName != CookieName || main.cookieName == experience.cookieName {
		t.Fatal("instance sessions collide")
	}
	for _, name := range []string{"other", "datamesh_session; Path=/", "datamesh_session\r\nX-Test: injected"} {
		if experience.ConfigureSessionCookie(name) == nil {
			t.Fatal("accepted invalid cookie namespace")
		}
		if experience.cookieName != "datamesh_experience_session" {
			t.Fatal("failed configuration changed the namespace")
		}
	}
	if err := experience.ConfigureSessionCookie(""); err != nil || experience.cookieName != CookieName {
		t.Fatal("default namespace changed")
	}
}
