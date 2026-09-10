package integrations

import (
	"fmt"
	"regexp"
	"strings"
)

// ConfigTransform is one pass over a config file body: either the next bytes
// and whether they changed, or a named refusal (fail-closed). A refusal with
// Retryable set is a conflict with user-owned bytes that a confirmed
// (forced) apply may take over; structural refusals stay final.
type ConfigTransform struct {
	Next      string
	Changed   bool
	Refused   string
	Retryable bool
}

func nextTransform(next string, changed bool) ConfigTransform {
	return ConfigTransform{Next: next, Changed: changed}
}

func refusedTransform(reason string) ConfigTransform {
	return ConfigTransform{Refused: reason}
}

func forceableTransform(reason string) ConfigTransform {
	return ConfigTransform{Refused: reason, Retryable: true}
}

const (
	OutcomeWritten   = "written"
	OutcomeUnchanged = "unchanged"
	OutcomeRefused   = "refused"
	OutcomeCrashed   = "crashed"
)

type WriteOutcome struct {
	Kind      string
	Reason    string
	Retryable bool
}
// ApplyConfigTransform is one apply pass over a config file: read, normalize
// EOL, transform (fail-closed via Refused), restore the dominant EOL, then a
// staged atomic write through io. crashBeforeRename stops after the temp file
// is durable and skips the rename, leaving the target at its last complete
// state — the seam the crash-safety verifier drives.
func ApplyConfigTransform(io FileIO, path string, transform func(currentLf string) ConfigTransform, crashBeforeRename bool) (WriteOutcome, error) {
	raw, _ := io.ReadTextIfExists(path)
	eol := DominantEol(raw)
	result := transform(ApplyEol(raw, EolLF))
	if result.Refused != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: result.Refused, Retryable: result.Retryable}, nil
	}
	if !result.Changed {
		return WriteOutcome{Kind: OutcomeUnchanged}, nil
	}
	if err := io.StageWrite(path, ApplyEol(result.Next, eol)); err != nil {
		return WriteOutcome{}, err
	}
	if crashBeforeRename {
		return WriteOutcome{Kind: OutcomeCrashed}, nil
	}
	if err := io.CommitStaged(path); err != nil {
		return WriteOutcome{}, err
	}
	return WriteOutcome{Kind: OutcomeWritten}, nil
}

func ProviderBaseUrl(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/v1", port)
}

// tomlString renders a TOML basic string with JSON.stringify-compatible escaping.
func tomlString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ToRollbackResult maps a rollback outcome; a no-change rollback is an honest
// not-managed refusal, never a pretend success.
func ToRollbackResult(id ID, outcome WriteOutcome) ApplyResult {
	if outcome.Kind == OutcomeUnchanged {
		return ApplyResult{OK: false, ID: id, Reason: "prism: " + string(id) + " is not managed by prism; rollback left the file untouched"}
	}
	return ToApplyResult(id, outcome)
}

func ToApplyResult(id ID, outcome WriteOutcome) ApplyResult {
	if outcome.Kind == OutcomeRefused {
		return ApplyResult{OK: false, ID: id, Reason: outcome.Reason, Retryable: outcome.Retryable}
	}
	if outcome.Kind == OutcomeCrashed {
		return ApplyResult{OK: false, ID: id, Reason: "prism: config write staged but not committed (crash simulation); recovery discards it"}
	}
	return ApplyResult{OK: true, ID: id}
}

var tomlStringFieldRe = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*` + key + `[ \t]*=[ \t]*"((?:[^"\\]|\\.)*)"[ \t]*(?:#.*)?$`)
}

var tomlBasicUnescapeRe = regexp.MustCompile(`\\(["\\])`)

// ParseTomlStringField finds the first `key = "value"` line in the text, with TOML basic-string escapes resolved.
func ParseTomlStringField(text string, key string) (string, bool) {
	m := tomlStringFieldRe(key).FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return tomlBasicUnescapeRe.ReplaceAllString(m[1], "$1"), true
}

const (
	ManagedAbsent      = "absent"
	ManagedPresent     = "present"
	ManagedDamaged     = "damaged"
	ManagedUnsupported = "unsupported"
)

type ManagedRead struct {
	Kind     string
	Endpoint *string
	Reason   string
}

// ObservedIntegrationStatus derives status from observable file facts:
// installed comes from file or detection-directory existence, managed only
// from Prism-owned bytes actually found in the target, endpoint from the
// managed bytes, and drift from comparing that endpoint with the requested one.
// ObservedIntegrationStatus derives status from observable file facts through io:
// installed comes from file or detection-directory existence, managed only
// from Prism-owned bytes actually found in the target, endpoint from the
// managed bytes, and drift from comparing that endpoint with the requested one.
func ObservedIntegrationStatus(io FileIO, id ID, targetPath string, detectDirs []string, read func(path string) ManagedRead, requestedEndpoint string) Status {
	configPresent := io.FileExists(targetPath)
	installed := configPresent
	for _, dir := range detectDirs {
		if io.FileExists(dir) {
			installed = true
			break
		}
	}
	if !configPresent {
		detail := "not installed"
		if installed {
			detail = "client detected, but the config file is not present yet"
		}
		return Status{ID: id, Installed: installed, Managed: false, TargetPath: strPtr(targetPath), Endpoint: nil, Drift: false, Detail: detail}
	}
	found := read(targetPath)
	switch found.Kind {
	case ManagedAbsent:
		return Status{ID: id, Installed: installed, Managed: false, TargetPath: strPtr(targetPath), Endpoint: nil, Drift: false, Detail: "installed; no prism-managed bytes present"}
	case ManagedDamaged, ManagedUnsupported:
		detail := found.Reason
		if detail == "" {
			detail = "prism-owned bytes could not be read"
		}
		return Status{ID: id, Installed: installed, Managed: found.Kind == ManagedDamaged, TargetPath: strPtr(targetPath), Endpoint: nil, Drift: false, Detail: detail}
	}
	drift := found.Endpoint != nil && *found.Endpoint != requestedEndpoint
	detail := "managed by prism"
	if drift {
		detail = fmt.Sprintf("managed endpoint %s drifted from the requested %s", *found.Endpoint, requestedEndpoint)
	}
	return Status{ID: id, Installed: installed, Managed: true, TargetPath: strPtr(targetPath), Endpoint: found.Endpoint, Drift: drift, Detail: detail}
}

func repeatSpaces(n int) string {
	return strings.Repeat(" ", n)
}

func joinStrings(lines []string) string {
	return strings.Join(lines, "\n")
}

func jsonLeafToManagedRead(read JSONLeafRead) ManagedRead {
	switch read.Kind {
	case jsonLeafAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case jsonLeafRefused:
		return ManagedRead{Kind: ManagedUnsupported, Reason: read.Reason}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: read.Endpoint}
}
