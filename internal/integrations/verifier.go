package integrations

import "strings"

type Probe interface {
	ID() ID
	Apply(crashBeforeRename bool) WriteOutcome
	Rollback() WriteOutcome
	Recover() bool
	Strip(content string) string
	MutateManagedRegion(content string) string
	HasManagedRegion(content string) bool
}

type Check struct {
	Semantic string
	OK       bool
	Detail   string
}

// VerifyReapplyIsStable checks that applying to a temporary copy twice reports no change the second time.
func VerifyReapplyIsStable(probe Probe, configPath string, seed string) Check {
	if err := AtomicWrite(LocalIO{}, configPath, seed); err != nil {
		return Check{Semantic: "reapply-stable", OK: false, Detail: "seed write failed"}
	}
	first := probe.Apply(false)
	if first.Kind != OutcomeWritten {
		return Check{Semantic: "reapply-stable", OK: false, Detail: "first apply did not write (" + first.Kind + ")"}
	}
	second := probe.Apply(false)
	if second.Kind != OutcomeUnchanged {
		return Check{Semantic: "reapply-stable", OK: false, Detail: "second apply reported " + second.Kind + ", expected no change"}
	}
	return Check{Semantic: "reapply-stable", OK: true, Detail: "second apply reported no change"}
}

// VerifyCrashRecovery checks that a crash between stage and rename keeps the
// last complete state, never a half-written file.
func VerifyCrashRecovery(probe Probe, configPath string, seed string) Check {
	if err := AtomicWrite(LocalIO{}, configPath, seed); err != nil {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "seed write failed"}
	}
	before, _ := LocalIO{}.ReadTextIfExists(configPath)
	crashed := probe.Apply(true)
	if crashed.Kind != OutcomeCrashed {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "crashing apply reported " + crashed.Kind}
	}
	midCrash, _ := LocalIO{}.ReadTextIfExists(configPath)
	if midCrash != before {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "target changed before the rename — atomicity broken"}
	}
	if !probe.Recover() {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "recovery pass found no staged temp to discard"}
	}
	afterRecovery, _ := LocalIO{}.ReadTextIfExists(configPath)
	if afterRecovery != before {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "recovery pass altered the last complete state"}
	}
	resumed := probe.Apply(false)
	if resumed.Kind != OutcomeWritten {
		return Check{Semantic: "crash-recovery", OK: false, Detail: "apply after recovery reported " + resumed.Kind}
	}
	return Check{Semantic: "crash-recovery", OK: true, Detail: "crash left the last complete state; recovery discarded the staged temp"}
}

// VerifyManagedRegionOwnership checks that the managed region is prism-owned:
// re-apply rewrites its content in place (whatever the difference), user bytes
// outside the region are untouched, and rollback removes exactly the region.
func VerifyManagedRegionOwnership(probe Probe, configPath string, seed string) Check {
	if err := AtomicWrite(LocalIO{}, configPath, seed); err != nil {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "seed write failed"}
	}
	applied := probe.Apply(false)
	if applied.Kind != OutcomeWritten {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "first apply reported " + applied.Kind}
	}
	edited, _ := LocalIO{}.ReadTextIfExists(configPath)
	edited = probe.MutateManagedRegion(edited)
	if err := AtomicWrite(LocalIO{}, configPath, edited); err != nil {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "edit write failed"}
	}
	reapplied := probe.Apply(false)
	if reapplied.Kind != OutcomeWritten {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "apply over a region difference reported " + reapplied.Kind}
	}
	current, _ := LocalIO{}.ReadTextIfExists(configPath)
	if current == edited {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "re-apply left the region content unchanged"}
	}
	if !probe.HasManagedRegion(current) {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "re-apply lost the managed region"}
	}
	rolledBack := probe.Rollback()
	if rolledBack.Kind != OutcomeWritten {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "rollback reported " + rolledBack.Kind}
	}
	restored, _ := LocalIO{}.ReadTextIfExists(configPath)
	if restored != probe.Strip(seed) {
		return Check{Semantic: "managed-region-ownership", OK: false, Detail: "rollback changed user bytes outside the managed region"}
	}
	return Check{Semantic: "managed-region-ownership", OK: true, Detail: "region rewritten in place; rollback removed exactly the region"}
}

// VerifyRollbackPreservesUserBytes checks that rollback removes exactly the
// managed region; user bytes outside it are preserved verbatim.
func VerifyRollbackPreservesUserBytes(probe Probe, configPath string, seed string) Check {
	if err := AtomicWrite(LocalIO{}, configPath, seed); err != nil {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "seed write failed"}
	}
	applied := probe.Apply(false)
	if applied.Kind != OutcomeWritten {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "apply reported " + applied.Kind}
	}
	rolledBack := probe.Rollback()
	if rolledBack.Kind != OutcomeWritten {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "rollback reported " + rolledBack.Kind}
	}
	restored, _ := LocalIO{}.ReadTextIfExists(configPath)
	if restored != seed {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "rollback did not restore the user bytes verbatim"}
	}
	if probe.HasManagedRegion(restored) {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "managed region survived rollback"}
	}
	again := probe.Rollback()
	if again.Kind != OutcomeUnchanged {
		return Check{Semantic: "rollback-exact", OK: false, Detail: "second rollback reported " + again.Kind}
	}
	return Check{Semantic: "rollback-exact", OK: true, Detail: "rollback removed exactly the managed region; user bytes verbatim"}
}

func VerifyIntegration(probe Probe, configPath string, seed string) []Check {
	return []Check{
		VerifyReapplyIsStable(probe, configPath, seed),
		VerifyCrashRecovery(probe, configPath, seed),
		VerifyManagedRegionOwnership(probe, configPath, seed),
		VerifyRollbackPreservesUserBytes(probe, configPath, seed),
	}
}

type fencedProbe struct {
	id       ID
	fence    Fence
	apply    func(crashBeforeRename bool) WriteOutcome
	rollback func() WriteOutcome
	recover  func() bool
}

func FencedProbe(id ID, fence Fence, apply func(crashBeforeRename bool) WriteOutcome, rollback func() WriteOutcome, recover func() bool) Probe {
	return &fencedProbe{id: id, fence: fence, apply: apply, rollback: rollback, recover: recover}
}

func (p *fencedProbe) ID() ID                        { return p.id }
func (p *fencedProbe) Apply(crash bool) WriteOutcome { return p.apply(crash) }
func (p *fencedProbe) Rollback() WriteOutcome        { return p.rollback() }
func (p *fencedProbe) Recover() bool                 { return p.recover() }
func (p *fencedProbe) Strip(content string) string {
	next, _ := RemoveFencedBlock(content, p.fence)
	return next
}
func (p *fencedProbe) MutateManagedRegion(content string) string {
	return strings.Replace(content, p.fence.Begin, p.fence.Begin+"\nuser_edit = true", 1)
}
func (p *fencedProbe) HasManagedRegion(content string) bool {
	return strings.Contains(content, p.fence.Begin)
}

type ompProbe struct {
	apply    func(crashBeforeRename bool) WriteOutcome
	rollback func() WriteOutcome
	recover  func() bool
	baseURL  string
}

func OmpProbe(apply func(crashBeforeRename bool) WriteOutcome, rollback func() WriteOutcome, recover func() bool, baseURL string) Probe {
	return &ompProbe{apply: apply, rollback: rollback, recover: recover, baseURL: baseURL}
}

func (p *ompProbe) ID() ID                        { return Omp }
func (p *ompProbe) Apply(crash bool) WriteOutcome { return p.apply(crash) }
func (p *ompProbe) Rollback() WriteOutcome        { return p.rollback() }
func (p *ompProbe) Recover() bool                 { return p.recover() }
func (p *ompProbe) Strip(content string) string {
	result := RemoveProviderLeaf(content, "prism")
	if result.Kind == "written" {
		return result.Next
	}
	return content
}
func (p *ompProbe) MutateManagedRegion(content string) string {
	return strings.Replace(content, "baseUrl: "+p.baseURL, "baseUrl: "+p.baseURL+"-mutated", 1)
}
func (p *ompProbe) HasManagedRegion(content string) bool {
	result := RemoveProviderLeaf(content, "prism")
	return result.Kind == "written" && result.Changed
}

// yamlProviderProbe adapts any `providers.<id>` YAML leaf client (omp, hermes).
type yamlProviderProbe struct {
	id          ID
	fileLabel   string
	endpointKey string
	baseURL     string
	apply       func(crashBeforeRename bool) WriteOutcome
	rollback    func() WriteOutcome
	recover     func() bool
}

func YAMLProviderProbe(id ID, fileLabel, endpointKey, baseURL string, apply func(crashBeforeRename bool) WriteOutcome, rollback func() WriteOutcome, recover func() bool) Probe {
	return &yamlProviderProbe{id: id, fileLabel: fileLabel, endpointKey: endpointKey, baseURL: baseURL, apply: apply, rollback: rollback, recover: recover}
}

func (p *yamlProviderProbe) ID() ID                        { return p.id }
func (p *yamlProviderProbe) Apply(crash bool) WriteOutcome { return p.apply(crash) }
func (p *yamlProviderProbe) Rollback() WriteOutcome        { return p.rollback() }
func (p *yamlProviderProbe) Recover() bool                 { return p.recover() }
func (p *yamlProviderProbe) Strip(content string) string {
	result := RemoveProviderLeafBody(content, "prism", p.fileLabel)
	if result.Kind == "written" {
		return result.Next
	}
	return content
}
func (p *yamlProviderProbe) MutateManagedRegion(content string) string {
	return strings.Replace(content, p.endpointKey+": "+p.baseURL, p.endpointKey+": "+p.baseURL+"-mutated", 1)
}
func (p *yamlProviderProbe) HasManagedRegion(content string) bool {
	return ReadProviderLeafBody(content, "prism", p.fileLabel, p.endpointKey).Kind == LeafPresent
}

type jsonBlockProbe struct {
	id          ID
	container   string
	fileLabel   string
	endpointKey string
	baseURL     string
	apply       func(crashBeforeRename bool) WriteOutcome
	rollback    func() WriteOutcome
	recover     func() bool
}

func JSONBlockProbe(id ID, container, fileLabel, endpointKey, baseURL string, apply func(crashBeforeRename bool) WriteOutcome, rollback func() WriteOutcome, recover func() bool) Probe {
	return &jsonBlockProbe{id: id, container: container, fileLabel: fileLabel, endpointKey: endpointKey, baseURL: baseURL, apply: apply, rollback: rollback, recover: recover}
}

func (p *jsonBlockProbe) ID() ID                        { return p.id }
func (p *jsonBlockProbe) Apply(crash bool) WriteOutcome { return p.apply(crash) }
func (p *jsonBlockProbe) Rollback() WriteOutcome        { return p.rollback() }
func (p *jsonBlockProbe) Recover() bool                 { return p.recover() }
func (p *jsonBlockProbe) Strip(content string) string {
	result := RemoveJSONBlockLeaf(content, p.container, "prism", p.fileLabel)
	if result.Kind == "written" {
		return result.Next
	}
	return content
}
func (p *jsonBlockProbe) MutateManagedRegion(content string) string {
	return strings.Replace(content, jsonString(p.endpointKey)+": "+jsonString(p.baseURL), jsonString(p.endpointKey)+": "+jsonString(p.baseURL+"-mutated"), 1)
}
func (p *jsonBlockProbe) HasManagedRegion(content string) bool {
	return ReadJSONBlockLeaf(content, p.container, "prism", p.fileLabel, p.endpointKey).Kind == jsonLeafPresent
}

// jsonScalarKeysProbe adapts the claude settings.json env-key client.
type jsonScalarKeysProbe struct {
	id          ID
	container   string
	fileLabel   string
	endpointKey string
	baseURL     string
	apply       func(crashBeforeRename bool) WriteOutcome
	rollback    func() WriteOutcome
	recover     func() bool
}

func JSONScalarKeysProbe(id ID, container, fileLabel, endpointKey, baseURL string, apply func(crashBeforeRename bool) WriteOutcome, rollback func() WriteOutcome, recover func() bool) Probe {
	return &jsonScalarKeysProbe{id: id, container: container, fileLabel: fileLabel, endpointKey: endpointKey, baseURL: baseURL, apply: apply, rollback: rollback, recover: recover}
}

func (p *jsonScalarKeysProbe) ID() ID                        { return p.id }
func (p *jsonScalarKeysProbe) Apply(crash bool) WriteOutcome { return p.apply(crash) }
func (p *jsonScalarKeysProbe) Rollback() WriteOutcome        { return p.rollback() }
func (p *jsonScalarKeysProbe) Recover() bool                 { return p.recover() }
func (p *jsonScalarKeysProbe) Strip(content string) string {
	result := RemoveJSONScalarKeys(content, p.container, p.fileLabel, claudeEnvKeyNames())
	if result.Kind == "written" {
		return result.Next
	}
	return content
}
func (p *jsonScalarKeysProbe) MutateManagedRegion(content string) string {
	return strings.Replace(content, jsonString(p.endpointKey)+": "+jsonString(p.baseURL), jsonString(p.endpointKey)+": "+jsonString(p.baseURL+"-mutated"), 1)
}
func (p *jsonScalarKeysProbe) HasManagedRegion(content string) bool {
	return ReadJSONScalarKeys(content, p.container, p.endpointKey, p.fileLabel, claudeEnvKeyNames()).Kind == jsonLeafPresent
}
