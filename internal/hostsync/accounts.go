package hostsync

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// Replica states, as the main machine reports them.
const (
	ReplicaActive      = "active"
	ReplicaPaused      = "paused"
	ReplicaNeedsReauth = "needs_reauth"
)

// Replica is one login account of the main machine as a host receives it.
// The main machine owns the account and its refresh grant; the host stores
// the credential under its own generation counter and never signs the
// account in or out. Credential is the account's credential blob. On a wire
// whose refresh token rotates it carries the access token only, so the host
// cannot spend the grant the main machine relies on. It is empty when the
// main machine has no credential to give.
type Replica struct {
	Provider   string          `json:"provider"`
	ID         string          `json:"id"`
	Email      string          `json:"email,omitempty"`
	State      string          `json:"state"`
	Priority   int             `json:"priority,omitempty"`
	Credential json.RawMessage `json:"credential,omitempty"`
}

// RejectedToken is a host's report that an upstream refused the access token
// of a replica. Hash is the token's SHA-256, so the token itself never leaves
// either machine through the report.
type RejectedToken struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Hash     string `json:"hash"`
}

// TokenHash is the hash a RejectedToken carries for access.
func TokenHash(access string) string {
	return hashSecret([]byte(access))
}

// RejectedTokens keeps, on a host, the replica access tokens an upstream
// refused until the main machine replaces them.
type RejectedTokens struct {
	mu   sync.Mutex
	seen map[string]RejectedToken
}

// Add records that the upstream refused access, the current token of the
// replica account of provider.
func (t *RejectedTokens) Add(provider, account, access string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]RejectedToken{}
	}
	t.seen[provider+"\x00"+account] = RejectedToken{Provider: provider, Account: account, Hash: TokenHash(access)}
}

// Pending lists the reports whose token is still the account's current one,
// sorted; current returns the hash of the account's current access token, or
// "" when it has none. Reports about a replaced token are dropped.
func (t *RejectedTokens) Pending(current func(provider, account string) string) []RejectedToken {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []RejectedToken
	for key, r := range t.seen {
		if current(r.Provider, r.Account) != r.Hash {
			delete(t.seen, key)
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b RejectedToken) int {
		if c := strings.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		return strings.Compare(a.Account, b.Account)
	})
	return out
}
