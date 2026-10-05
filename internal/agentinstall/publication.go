package agentinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type executableIdentity struct {
	entry, real string
	info        os.FileInfo
}

type scriptPublication struct {
	entries []string
	before  []executableIdentity
}

func (m *Manager) executableIdentity(entry string) executableIdentity {
	identity := executableIdentity{entry: entry}
	identity.info, _ = m.stat(entry)
	identity.real, _ = m.eval(entry)
	return identity
}

func (m *Manager) scriptPublication(def Definition, entry string) scriptPublication {
	publication := scriptPublication{entries: []string{entry}}
	for _, dir := range m.candidateDirs(def) {
		if dir == "" {
			continue
		}
		identity := m.executableIdentity(absolute(filepath.Join(dir, def.Binary)))
		if identity.info != nil {
			publication.before = append(publication.before, identity)
		}
	}
	for _, candidate := range publication.entries {
		identity := m.executableIdentity(candidate)
		if identity.info != nil {
			publication.before = append(publication.before, identity)
		}
	}
	return publication
}

func (m *Manager) publishedTarget(def Definition, publication scriptPublication) (installTarget, error) {
	var targets []installTarget
	for _, entry := range publication.entries {
		if def.Key == "pi" && m.env["PI_LEGACY_INSTALL"] != "1" {
			managed, ok := readPiManagedInstall(piManagedRoot(m.env))
			if !ok || managed.entry != entry {
				continue
			}
		}
		obs := m.observeEntry(def, entry)
		if obs.entry == "" || obs.reason != "" {
			continue
		}
		identity := m.executableIdentity(entry)
		changed := identity.info != nil
		for _, before := range publication.before {
			if identity.info == nil || before.info == nil {
				continue
			}
			if os.SameFile(identity.info, before.info) {
				if before.entry != entry || before.real == identity.real && before.info.ModTime() == identity.info.ModTime() && before.info.Size() == identity.info.Size() {
					changed = false
				}
			}
		}
		if changed {
			targets = append(targets, obs.target)
		}
	}
	if len(targets) != 1 {
		return installTarget{}, fmt.Errorf("installer produced no unique new or changed executable at its intended destination %s", strings.Join(publication.entries, ", "))
	}
	return targets[0], nil
}
