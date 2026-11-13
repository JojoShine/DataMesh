package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/platform/database"
)

func TestHealthcheckValidatesTLSJSONAndPrefix(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(t.TempDir(), "server.crt")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	status, body := http.StatusOK, `{"status":"ready"}`
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datamesh/healthz" {
			t.Error("lost prefix")
		}
		w.Header().Set("Location", "/")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	t.Setenv("PORT", strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port))
	t.Setenv("BASE_PATH", "/datamesh")
	t.Setenv("TLS_CERT", certFile)
	if err := healthcheck(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		status int
		body   string
	}{{200, "<html>ready</html>"}, {200, `{"status":"alive"}`}, {503, `{"status":"ready"}`}, {302, `{"status":"ready"}`}} {
		status, body = test.status, test.body
		if healthcheck() == nil {
			t.Fatalf("accepted false readiness: %d %s", status, body)
		}
	}
	status, body = 200, `{"status":"ready"}`
	t.Setenv("TLS_CERT", "")
	if healthcheck() == nil {
		t.Fatal("accepted untrusted TLS")
	}
}

func TestRuntimePostgresMigrationBootstrap(t *testing.T) {
	dsn := os.Getenv("RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires an explicitly isolated PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	connectionURL, parseErr := url.Parse(dsn)
	isolatedHost := parseErr == nil && strings.HasPrefix(connectionURL.Hostname(), "datamesh-runtime-test-") && name == "datamesh"
	if !strings.HasPrefix(name, "datamesh_runtime_test_") && !isolatedHost {
		t.Fatal("refusing a non-owned test database")
	}
	t.Setenv("MIGRATION_DATABASE_URL", dsn)
	applicationDSN := os.Getenv("RUNTIME_TEST_APP_DATABASE_URL")
	if applicationDSN == "" {
		t.Fatal("explicit isolated application role is required")
	}
	t.Setenv("DATABASE_URL", applicationDSN)
	if err := migrateDatabase(); err != nil {
		t.Fatal(err)
	}
	if err := migrateDatabase(); err != nil {
		t.Fatal(err)
	}
	var canRead bool
	if err := db.Raw("SELECT has_table_privilege('datamesh_app','public.schema_migrations','SELECT')").Scan(&canRead).Error; err != nil || !canRead {
		t.Fatal("application cannot verify schema readiness")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSRF_KEY", hex.EncodeToString(secret))
	file := filepath.Join(t.TempDir(), "bootstrap.json")
	t.Setenv("BOOTSTRAP_ADMIN_FILE", file)
	write := func(password string) {
		raw, _ := json.Marshal(bootstrapInput{Username: "runtime_admin", Password: password, DepartmentID: "runtime-private"})
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("runtime-password-first-0123456789")
	if err := bootstrap(); err != nil {
		t.Fatal(err)
	}
	var initialHash string
	if err := db.Raw("SELECT password_hash FROM users WHERE username='runtime_admin'").Scan(&initialHash).Error; err != nil || initialHash == "" {
		t.Fatal("missing initial administrator")
	}
	write("runtime-password-second-0123456789")
	if err := bootstrap(); err != nil {
		t.Fatal(err)
	}
	var currentHash string
	if err := db.Raw("SELECT password_hash FROM users WHERE username='runtime_admin'").Scan(&currentHash).Error; err != nil || currentHash != initialHash {
		t.Fatal("repeat bootstrap changed credentials")
	}
	var admins int64
	if err := db.Raw("SELECT count(*) FROM user_roles WHERE role='admin'").Scan(&admins).Error; err != nil || admins != 1 {
		t.Fatal("bootstrap duplicated administrator")
	}
	if err := db.Exec("UPDATE users SET active=false WHERE username='runtime_admin'").Error; err != nil {
		t.Fatal(err)
	}
	if bootstrap() == nil {
		t.Fatal("bootstrap revived a disabled identity")
	}
}
