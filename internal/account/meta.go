package account

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"prism/internal/quota"
)

const metaSchemaVersion = 1

const (
	metaStateActive      = "active"
	metaStatePaused      = "paused"
	metaStateNeedsReauth = "needs_reauth"
)

func metaStateFor(state State) string {
	switch state {
	case Paused:
		return metaStatePaused
	case NeedsReauth:
		return metaStateNeedsReauth
	default:
		return metaStateActive
	}
}

type metaFile struct {
	Version  int          `json:"version"`
	Accounts []metaRecord `json:"accounts"`
}

type metaRecord struct {
	ID        string     `json:"id"`
	Provider  string     `json:"provider"`
	Priority  int        `json:"priority"`
	CredGen   uint64     `json:"credGen"`
	State     string     `json:"state"`
	Email     string     `json:"email,omitempty"`
	Quota     *metaQuota `json:"quota,omitempty"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
}

type metaQuota struct {
	Used      int64  `json:"used"`
	Limit     *int64 `json:"limit,omitempty"`
	WindowEnd string `json:"windowEnd,omitempty"`
	Source    string `json:"source,omitempty"`
}

var metaQuotaSources = map[string]quota.Source{
	"header":   quota.SourceHeader,
	"endpoint": quota.SourceEndpoint,
	"report":   quota.SourceReport,
	"probe":    quota.SourceProbe,
}

func (q metaQuota) snapshot() (quota.Snapshot, error) {
	s := quota.Snapshot{Used: q.Used, Limit: q.Limit}
	if q.WindowEnd != "" {
		t, err := time.Parse(time.RFC3339, q.WindowEnd)
		if err != nil {
			return quota.Snapshot{}, fmt.Errorf("quota windowEnd %q: %w", q.WindowEnd, err)
		}
		s.WindowEnd = t
	}
	if q.Source != "" {
		src, ok := metaQuotaSources[q.Source]
		if !ok {
			return quota.Snapshot{}, fmt.Errorf("unknown quota source %q", q.Source)
		}
		s.Source = src
	}
	return s, nil
}

// Repository owns the durable non-secret account metadata file for one provider.
type Repository struct {
	path string
	now  func() time.Time
}

func OpenMeta(path string) *Repository {
	return &Repository{path: path, now: time.Now}
}

func (r *Repository) Load(providerID ProviderID) ([]Account, error) {
	records, err := r.read(providerID)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(records))
	for _, rec := range records {
		a, err := rec.account(providerID)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (r *Repository) CurrentGeneration(providerID ProviderID, id AccountID) (CredentialGeneration, error) {
	records, err := r.read(providerID)
	if err != nil {
		return 0, err
	}
	for _, rec := range records {
		if rec.ID == string(id) {
			return CredentialGeneration(rec.CredGen), nil
		}
	}
	return 0, nil
}

// CASGeneration moves the credential generation of id from fromGen to toGen.
// The row must currently reference fromGen; anything else is a stale writer.
// fromGen 0 with no existing row inserts a fresh record.
func (r *Repository) CASGeneration(providerID ProviderID, id AccountID, fromGen, toGen CredentialGeneration, email string) error {
	if toGen == 0 {
		return fmt.Errorf("accounts %s: credential generation must be positive", r.path)
	}
	records, err := r.read(providerID)
	if err != nil {
		return err
	}
	now := r.now().UTC().Format(time.RFC3339)
	for i := range records {
		if records[i].ID != string(id) {
			continue
		}
		if records[i].CredGen != uint64(fromGen) {
			return ErrStale
		}
		records[i].CredGen = uint64(toGen)
		if email != "" {
			records[i].Email = email
		}
		records[i].UpdatedAt = now
		return r.write(metaFile{Version: metaSchemaVersion, Accounts: records})
	}
	if fromGen == 0 {
		records = append(records, metaRecord{
			ID:        string(id),
			Provider:  string(providerID),
			CredGen:   uint64(toGen),
			State:     metaStateActive,
			Email:     email,
			CreatedAt: now,
			UpdatedAt: now,
		})
		return r.write(metaFile{Version: metaSchemaVersion, Accounts: records})
	}
	return ErrNotFound
}

// Ensure upserts the non-secret metadata row for id at the given credential
// generation, creating the row when absent. Priority, state, quota, email, and
// creation time of an existing row are preserved.
func (r *Repository) Ensure(providerID ProviderID, id AccountID, gen CredentialGeneration, email string) error {
	if gen == 0 {
		return fmt.Errorf("accounts %s: credential generation must be positive", r.path)
	}
	records, err := r.read(providerID)
	if err != nil {
		return err
	}
	now := r.now().UTC().Format(time.RFC3339)
	for i := range records {
		if records[i].ID != string(id) {
			continue
		}
		records[i].CredGen = uint64(gen)
		if email != "" {
			records[i].Email = email
		}
		records[i].UpdatedAt = now
		return r.write(metaFile{Version: metaSchemaVersion, Accounts: records})
	}
	records = append(records, metaRecord{
		ID:        string(id),
		Provider:  string(providerID),
		CredGen:   uint64(gen),
		State:     metaStateActive,
		Email:     email,
		CreatedAt: now,
		UpdatedAt: now,
	})
	return r.write(metaFile{Version: metaSchemaVersion, Accounts: records})
}

// SetState updates the durable state of an existing account row. The row must
// already exist; credential generations and metadata are untouched.
func (r *Repository) SetState(providerID ProviderID, id AccountID, state State) error {
	records, err := r.read(providerID)
	if err != nil {
		return err
	}
	found := false
	for i := range records {
		if records[i].ID != string(id) {
			continue
		}
		records[i].State = metaStateFor(state)
		records[i].UpdatedAt = r.now().UTC().Format(time.RFC3339)
		found = true
	}
	if !found {
		return fmt.Errorf("accounts %s: account %s not found", r.path, id)
	}
	return r.write(metaFile{Version: metaSchemaVersion, Accounts: records})
}

func (r *Repository) read(providerID ProviderID) ([]metaRecord, error) {
	b, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("accounts %s: %w", r.path, err)
	}
	var file metaFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if file.Version != metaSchemaVersion {
		return nil, fmt.Errorf("accounts %s: unsupported version %d", r.path, file.Version)
	}
	seen := make(map[string]bool, len(file.Accounts))
	for i := range file.Accounts {
		rec := &file.Accounts[i]
		if rec.ID == "" {
			return nil, fmt.Errorf("accounts %s: empty account id", r.path)
		}
		if seen[rec.ID] {
			return nil, fmt.Errorf("accounts %s: duplicate account id %q", r.path, rec.ID)
		}
		seen[rec.ID] = true
		if rec.Provider != string(providerID) {
			return nil, fmt.Errorf("accounts %s: account %q belongs to provider %q, want %q", r.path, rec.ID, rec.Provider, providerID)
		}
		if rec.CredGen == 0 {
			return nil, fmt.Errorf("accounts %s: account %q has no credential generation", r.path, rec.ID)
		}
		if rec.State != metaStateActive && rec.State != metaStatePaused && rec.State != metaStateNeedsReauth {
			return nil, fmt.Errorf("accounts %s: account %q has unknown state %q", r.path, rec.ID, rec.State)
		}
		if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("accounts %s: account %q createdAt: %w", r.path, rec.ID, err)
		}
		if _, err := time.Parse(time.RFC3339, rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("accounts %s: account %q updatedAt: %w", r.path, rec.ID, err)
		}
		if rec.Quota != nil {
			if _, err := rec.Quota.snapshot(); err != nil {
				return nil, fmt.Errorf("accounts %s: account %q: %w", r.path, rec.ID, err)
			}
		}
	}
	return file.Accounts, nil
}

func (r *Repository) write(file metaFile) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	tmp, err := os.CreateTemp(dir, ".accounts-*")
	if err != nil {
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if err := os.Rename(name, r.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("accounts %s: %w", r.path, err)
	}
	if handle, err := os.Open(dir); err == nil {
		handle.Sync()
		handle.Close()
	}
	return nil
}

func (rec metaRecord) account(providerID ProviderID) (Account, error) {
	q := quota.Snapshot{}
	if rec.Quota != nil {
		var err error
		if q, err = rec.Quota.snapshot(); err != nil {
			return Account{}, err
		}
	}
	state := Active
	if rec.State == metaStatePaused {
		state = Paused
	}
	if rec.State == metaStateNeedsReauth {
		state = NeedsReauth
	}
	return Account{
		ID:       AccountID(rec.ID),
		Provider: providerID,
		Priority: rec.Priority,
		State:    state,
		CredGen:  CredentialGeneration(rec.CredGen),
		Quota:    q,
	}, nil
}
