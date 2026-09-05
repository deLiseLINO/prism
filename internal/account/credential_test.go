package account

import (
	"strings"
	"testing"
	"time"
)

func TestCredentialEncodeParseRoundTrip(t *testing.T) {
	cred := Credential{
		Access:    "at",
		Refresh:   "rt",
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
		AccountID: "acc-1",
		Email:     "u@e.co",
		ProjectID: "proj-1",
	}
	parsed, err := ParseCredential(cred.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed != cred {
		t.Fatalf("round trip = %+v, want %+v", parsed, cred)
	}
}

func TestCredentialEncodeOmitsEmptyFields(t *testing.T) {
	raw := string(Credential{Access: "at"}.Encode())
	for _, unwanted := range []string{"refresh", "expiresAt", "accountID", "email", "projectID"} {
		if strings.Contains(raw, `"`+unwanted+`"`) {
			t.Fatalf("key %q present in %s", unwanted, raw)
		}
	}
	if !strings.Contains(raw, `"access":"at"`) {
		t.Fatalf("access missing: %s", raw)
	}
}

func TestParseCredentialVersionedBlob(t *testing.T) {
	blob := `{"version":1,"access":"at","refresh":"rt","expiresAt":"2030-01-01T00:00:00Z","accountID":"a1","email":"u@e.co","projectID":"p1"}`
	cred, err := ParseCredential([]byte(blob))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Access != "at" || cred.Refresh != "rt" || cred.AccountID != "a1" || cred.Email != "u@e.co" || cred.ProjectID != "p1" {
		t.Fatalf("cred = %+v", cred)
	}
	if cred.ExpiresAt != time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("expires = %v", cred.ExpiresAt)
	}
}

func TestParseCredentialRejectsUnknownVersion(t *testing.T) {
	if _, err := ParseCredential([]byte(`{"version":99,"access":"at"}`)); err == nil {
		t.Fatal("expected version error")
	}
}

func TestParseCredentialLegacyCodexShape(t *testing.T) {
	cred, err := ParseCredential([]byte(`{"AccessToken":"at","ChatGPTAccountID":"acc-1"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Access != "at" || cred.AccountID != "acc-1" || cred.Refresh != "" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestParseCredentialLegacyAntigravityShape(t *testing.T) {
	cred, err := ParseCredential([]byte(`{"AccessToken":"at","ProjectID":"proj-1"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Access != "at" || cred.ProjectID != "proj-1" || cred.AccountID != "" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestParseCredentialPlainToken(t *testing.T) {
	cred, err := ParseCredential([]byte("sk-plain-token-value"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Access != "sk-plain-token-value" || cred.Refresh != "" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestParseCredentialLegacyEmptyObject(t *testing.T) {
	cred, err := ParseCredential([]byte(`{}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Access != "" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestParseCredentialRejectsBadExpiry(t *testing.T) {
	if _, err := ParseCredential([]byte(`{"version":1,"access":"at","expiresAt":"not-a-time"}`)); err == nil {
		t.Fatal("expected expiry error")
	}
}
