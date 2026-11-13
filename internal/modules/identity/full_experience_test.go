package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredFullExperienceRequiresPersistentMarker(t *testing.T) {
	s := &Service{fullExperienceAccounts: []configuredTestAccount{{PublicTestAccount: PublicTestAccount{Username: "public_user", Mode: "full"}, Version: 2}}}
	for _, role := range []string{"consumer", "admin"} {
		_, err := s.projectFullExperience(t.Context(), Principal{Username: "public_user", Roles: []string{role}, Platforms: []string{"assets"}})
		if !errors.Is(err, ErrCredentials) {
			t.Fatalf("configured public identity without marker authenticated as %s", role)
		}
	}
	p, err := s.projectFullExperience(t.Context(), Principal{Username: "private_admin", Roles: []string{"admin"}, Platforms: []string{"security"}})
	if err != nil || p.Username != "private_admin" || p.FullExperience || p.PublicTest {
		t.Fatal("unconfigured private admin affected")
	}
}

func TestPublicTestAccountConfigurationCountBoundary(t *testing.T) {
	for _, count := range []int{4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			items := make([]map[string]interface{}, count)
			for i := range items {
				items[i] = map[string]interface{}{"username": fmt.Sprintf("public_user_%d", i), "password": "dedicated-test-password", "label": "Experience", "version": 2, "mode": "full"}
			}
			raw, err := json.Marshal(map[string]interface{}{"items": items})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "accounts.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(&Service{}, "https://example.test")
			if err := h.ConfigurePublicTestAccounts(path); (err == nil) != (count == 4) {
				t.Fatalf("configuration with %d accounts validity = %v", count, err == nil)
			}
		})
	}
}

func TestFullExperienceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		valid bool
	}{
		{``, true}, {`,"mode":"readonly"`, true}, {`,"mode":"full"`, true},
		{`,"mode":"FULL"`, false}, {`,"mode":""`, false}, {`,"mode":null`, false}, {`,"mode":true`, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounts.json")
			body := `{"items":[{"username":"public_user","password":"dedicated-test-password","label":"Experience","version":2` + tc.mode + `}]}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(&Service{}, "https://example.test")
			if err := h.ConfigurePublicTestAccounts(path); (err == nil) != tc.valid {
				t.Fatalf("configuration validity = %v, want %v", err == nil, tc.valid)
			}
		})
	}
}

func TestFullExperienceAccessEligibility(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mode             experienceMode
		roles, platforms []string
		want             bool
	}{
		{"legacy", "", []string{"consumer"}, []string{"exchange", "assets"}, true},
		{"legacy business role", "", []string{"operator"}, []string{"assets"}, false},
		{"business", "full", []string{"operator", "governor", "provider", "consumer", "auditor"}, []string{"exchange", "assets", "governance", "security"}, true},
		{"admin", "full", []string{"consumer", "admin"}, []string{"assets"}, false},
		{"unknown role", "full", []string{"directory_reviewer"}, []string{"assets"}, false},
		{"empty roles", "full", nil, []string{"assets"}, false},
		{"empty platforms", "full", []string{"consumer"}, nil, false},
		{"unknown platform", "full", []string{"consumer"}, []string{"other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validExperienceAccess(tc.mode, tc.roles, tc.platforms) != tc.want {
				t.Fatal("unexpected eligibility")
			}
		})
	}
}
