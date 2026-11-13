package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const experienceCookie = "datamesh_experience_session"

func ingressTransport(certificate []byte) (*http.Transport, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("invalid experience upstream certificate")
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 32, MaxIdleConnsPerHost: 16}, nil
}

// The application has no external route; this ingress only reaches its fixed upstream.
func ingressHandler(target *url.URL, transport http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(p *httputil.ProxyRequest) {
		for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Cookie"} {
			p.Out.Header.Del(name)
		}
		p.SetURL(target)
		p.Out.Host = p.In.Host
		p.SetXForwarded()
		// Cookie scope does not include ports: keep main-instance cookies out of this backend.
		for _, cookie := range p.In.Cookies() {
			if cookie.Name == experienceCookie {
				p.Out.AddCookie(cookie)
			}
		}
	}, ModifyResponse: func(response *http.Response) error {
		cookies := response.Cookies()
		response.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			if cookie.Name == experienceCookie {
				cookie.Domain = ""
				cookie.Path = "/"
				cookie.Secure = true
				cookie.HttpOnly = true
				cookie.SameSite = http.SameSiteStrictMode
				response.Header.Add("Set-Cookie", cookie.String())
			}
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"code":"EXPERIENCE_UPSTREAM_UNAVAILABLE","message":"体验服务暂时无法连接，请稍后再试"}`)
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cleaned := path.Clean(r.URL.Path); cleaned == "/internal" || strings.HasPrefix(cleaned, "/internal/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodConnect || r.Method == http.MethodTrace {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.IsAbs() || r.URL.Opaque != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
func run() error {
	ca := os.Getenv("INGRESS_CA")
	if ca == "" {
		ca = os.Getenv("TLS_CERT")
	}
	file, err := os.Open(ca)
	if err != nil {
		return errors.New("experience upstream certificate unavailable")
	}
	defer file.Close()
	certificate, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(certificate) > 1<<20 {
		return errors.New("invalid experience upstream certificate")
	}
	transport, err := ingressTransport(certificate)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	target, _ := url.Parse("https://server:8443")
	server := &http.Server{Addr: ":8443", Handler: ingressHandler(target, transport), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 130 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	return server.ListenAndServeTLS(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"))
}
func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("experience ingress stopped", "error", err.Error())
		os.Exit(1)
	}
}
