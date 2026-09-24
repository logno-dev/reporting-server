package apiclients

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestParseKey(t *testing.T) {
	id := ulid.Make().String()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	keyID, parsed, err := ParseKey("rpt_live_" + id + "_" + encoded)
	if err != nil || keyID != id || string(parsed) != string(secret) {
		t.Fatalf("ParseKey() = %q, %x, %v", keyID, parsed, err)
	}
}

func TestParseKeyRejectsMalformedValues(t *testing.T) {
	id := ulid.Make().String()
	values := []string{"", "other_" + id, "rpt_live_bad_secret", "rpt_live_" + id + "_not+base64", "rpt_live_" + id + "_" + base64.RawURLEncoding.EncodeToString(make([]byte, 31)), "rpt_live_" + id + "_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + "_extra"}
	for _, value := range values {
		if _, _, err := ParseKey(value); err == nil {
			t.Errorf("ParseKey(%q) succeeded", value)
		}
	}
}

func TestValidateScopes(t *testing.T) {
	got, err := ValidateScopes([]string{ScopeReportsRead, ScopeTemplatesRead})
	if err != nil || strings.Join(got, ",") != ScopeReportsRead+","+ScopeTemplatesRead {
		t.Fatalf("ValidateScopes() = %v, %v", got, err)
	}
	for _, scopes := range [][]string{{}, {"admin"}, {ScopeReportsRead, ScopeReportsRead}} {
		if _, err := ValidateScopes(scopes); err == nil {
			t.Errorf("ValidateScopes(%v) succeeded", scopes)
		}
	}
}

func TestRedactedKeyDoesNotMarshalSecretMaterial(t *testing.T) {
	payload, err := json.Marshal(Key{ID: ulid.Make().String(), Prefix: "rpt_live_safe_", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "hash") || strings.Contains(string(payload), "secret") {
		t.Fatalf("redacted key leaked secret material: %s", payload)
	}
}
