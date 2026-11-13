package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/platform/database"
	"datamesh/internal/platform/webpath"
	"datamesh/migrations"
	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
)

func migrateDatabase() error {
	dsn := os.Getenv("MIGRATION_DATABASE_URL")
	if dsn == "" {
		return errors.New("migration connection is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("migration connection is invalid")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("migration database unavailable")
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return errors.New("migration role or database unavailable")
	}
	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return errors.New("embedded migrations unavailable")
	}
	runner, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return errors.New("cannot initialize migration runner")
	}
	defer runner.Close()
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migration failed: %w", err)
	}
	version, dirty, err := runner.Version()
	want, metadataErr := migrations.Version()
	if err != nil || metadataErr != nil || dirty || version != want {
		return errors.New("schema version is not ready")
	}
	grantContext, grantCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer grantCancel()
	if _, err := db.ExecContext(grantContext, "GRANT SELECT ON TABLE public.schema_migrations TO datamesh_app"); err != nil {
		return errors.New("cannot grant application readiness access")
	}
	fmt.Printf("schema %d ready\n", version)
	return nil
}

type bootstrapInput struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	DepartmentID string `json:"department_id"`
	DisplayName  string `json:"display_name"`
}

func bootstrap() error {
	path := os.Getenv("BOOTSTRAP_ADMIN_FILE")
	if path == "" {
		return errors.New("bootstrap requires a private account file")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("bootstrap file unavailable")
	}
	defer file.Close()
	var input bootstrapInput
	decoder := json.NewDecoder(io.LimitReader(file, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return errors.New("invalid bootstrap account")
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("bootstrap requires one account")
	}
	key, err := hex.DecodeString(os.Getenv("CSRF_KEY"))
	if err != nil || len(key) < 32 {
		return errors.New("invalid CSRF configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("bootstrap database unavailable")
	}
	connection, err := db.DB()
	if err != nil {
		return errors.New("bootstrap database unavailable")
	}
	defer connection.Close()
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('datamesh/bootstrap', 0))").Error; err != nil {
			return err
		}
		repo := identity.NewRepository(tx)
		user, err := repo.FindUser(ctx, input.Username)
		if err == nil {
			var count int64
			if err := tx.Model(&identity.UserRole{}).Where("user_id = ? AND role = 'admin'", user.ID).Count(&count).Error; err != nil {
				return err
			}
			if !user.Active || user.PublicTest || count != 1 {
				return errors.New("existing identity is not an active private administrator")
			}
			return nil
		}
		if !identity.IsMissing(err) {
			return err
		}
		service, err := identity.NewService(repo, key)
		if err != nil {
			return err
		}
		_, err = service.CreateUser(ctx, identity.NewUser{Username: input.Username, Password: input.Password, DepartmentID: input.DepartmentID, DisplayName: input.DisplayName, Roles: []string{"admin"}, Platforms: []string{"exchange", "governance", "assets", "security"}})
		return err
	})
	input.Password = ""
	if err != nil {
		return errors.New("bootstrap failed; existing account and data preserved")
	}
	fmt.Println("administrator initialized; existing credentials preserved")
	return nil
}

func healthcheck() error {
	base, err := webpath.Normalize(os.Getenv("BASE_PATH"))
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8443"
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return errors.New("system certificate store unavailable")
	}
	if cert := os.Getenv("TLS_CERT"); cert != "" {
		pem, err := os.ReadFile(cert)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return errors.New("healthcheck certificate unavailable")
		}
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("https://localhost:" + port + base + "/healthz")
	if err != nil {
		return errors.New("application not ready")
	}
	defer response.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&body) != nil || body.Status != "ready" {
		return errors.New("application not ready")
	}
	return nil
}

func main() {
	name := filepath.Base(os.Args[0])
	if name == "runtime-tools" && len(os.Args) == 2 {
		name = os.Args[1]
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "unexpected task arguments")
		os.Exit(1)
	}
	var err error
	switch name {
	case "migrate":
		err = migrateDatabase()
	case "bootstrap":
		err = bootstrap()
	case "healthcheck":
		err = healthcheck()
	default:
		err = errors.New("unknown runtime task")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
