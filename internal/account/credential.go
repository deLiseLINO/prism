package account

import (
	"encoding/json"
	"fmt"
	"time"
)

const CredentialVersion = 1

type Credential struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
	AccountID string
	Email     string
	ProjectID string
}

type credentialBlob struct {
	Version   int    `json:"version"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	AccountID string `json:"accountID,omitempty"`
	Email     string `json:"email,omitempty"`
	ProjectID string `json:"projectID,omitempty"`
}

func (c Credential) Encode() []byte {
	b := credentialBlob{
		Version:   CredentialVersion,
		Access:    c.Access,
		Refresh:   c.Refresh,
		AccountID: c.AccountID,
		Email:     c.Email,
		ProjectID: c.ProjectID,
	}
	if !c.ExpiresAt.IsZero() {
		b.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return []byte(`{"version":1}`)
	}
	return raw
}

// ParseCredential decodes every credential blob shape prismd has ever written:
// the versioned structured blob, the earlier unversioned structured encodings,
// and plain single-token blobs.
func ParseCredential(blob []byte) (Credential, error) {
	var versioned struct {
		Version   *int   `json:"version"`
		Access    string `json:"access"`
		Refresh   string `json:"refresh"`
		ExpiresAt string `json:"expiresAt"`
		AccountID string `json:"accountID"`
		Email     string `json:"email"`
		ProjectID string `json:"projectID"`
	}
	if err := json.Unmarshal(blob, &versioned); err == nil && versioned.Version != nil {
		if *versioned.Version != CredentialVersion {
			return Credential{}, fmt.Errorf("account: unsupported credential version %d", *versioned.Version)
		}
		c := Credential{
			Access:    versioned.Access,
			Refresh:   versioned.Refresh,
			AccountID: versioned.AccountID,
			Email:     versioned.Email,
			ProjectID: versioned.ProjectID,
		}
		if versioned.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, versioned.ExpiresAt)
			if err != nil {
				return Credential{}, fmt.Errorf("account: credential expiresAt: %w", err)
			}
			c.ExpiresAt = t
		}
		return c, nil
	}
	var legacy struct {
		AccessToken      string `json:"AccessToken"`
		ChatGPTAccountID string `json:"ChatGPTAccountID"`
		ProjectID        string `json:"ProjectID"`
	}
	if err := json.Unmarshal(blob, &legacy); err == nil {
		return Credential{
			Access:    legacy.AccessToken,
			AccountID: legacy.ChatGPTAccountID,
			ProjectID: legacy.ProjectID,
		}, nil
	}
	return Credential{Access: string(blob)}, nil
}
