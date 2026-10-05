package agentinstall

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/deLiseLINO/prism/internal/integrations"
)

type piManagedInstall struct {
	root, launcher, entry string
}

func readPiManagedInstall(root string) (piManagedInstall, bool) {
	if !filepath.IsAbs(root) || filepath.Base(root) != "install" {
		return piManagedInstall{}, false
	}
	root = absolute(root)
	path := filepath.Join(root, "managed-install.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return piManagedInstall{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return piManagedInstall{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return piManagedInstall{}, false
	}
	var marker struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		Layout        string `json:"layout"`
		Entrypoint    struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"entrypoint"`
	}
	if json.Unmarshal(data, &marker) != nil || marker.Kind != "pi-managed-install" || marker.SchemaVersion != 1 || marker.Layout != "releases-v1" || !filepath.IsAbs(marker.Entrypoint.Path) || filepath.Base(marker.Entrypoint.Path) != "pi" || filepath.Clean(marker.Entrypoint.Path) != marker.Entrypoint.Path {
		return piManagedInstall{}, false
	}
	launcher := filepath.Join(filepath.Dir(root), "bin", "pi")
	entry := absolute(marker.Entrypoint.Path)
	if marker.Entrypoint.Type == "script" {
		if entry != launcher {
			return piManagedInstall{}, false
		}
	} else if marker.Entrypoint.Type != "symlink" || entry == launcher {
		return piManagedInstall{}, false
	}
	return piManagedInstall{root: root, launcher: launcher, entry: entry}, true
}

func piManagedRoot(env integrations.Env) string {
	if configured, ok := readPiManagedInstall(env["PI_MANAGED_INSTALL_ROOT"]); ok {
		return configured.root
	}
	return absolute(filepath.Join(valueOr(env, "PI_CODING_AGENT_DIR", filepath.Join(env["HOME"], ".pi", "agent")), "install"))
}

func piManagedDirs(env integrations.Env) []string {
	root := piManagedRoot(env)
	dirs := []string{filepath.Join(filepath.Dir(root), "bin")}
	if managed, ok := readPiManagedInstall(root); ok {
		dirs = append(dirs, filepath.Dir(managed.entry))
	}
	return dirs
}

func recognizePiManaged(env integrations.Env, entry, real string) (installTarget, bool) {
	roots := []string{piManagedRoot(env)}
	if filepath.Base(real) == "pi" && filepath.Base(filepath.Dir(real)) == "bin" {
		roots = append(roots, filepath.Join(filepath.Dir(filepath.Dir(real)), "install"))
	}
	for _, root := range roots {
		managed, ok := readPiManagedInstall(root)
		if !ok || real != managed.launcher || entry != managed.entry && entry != managed.launcher {
			continue
		}
		if entry != managed.launcher {
			info, err := os.Lstat(entry)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}
		}
		return installTarget{source: SourceScript, entry: entry, root: managed.root, name: "pi-managed"}, true
	}
	return installTarget{}, false
}

func writableDirectory(path string, stat func(string) (os.FileInfo, error)) bool {
	for filepath.IsAbs(path) {
		info, err := stat(path)
		if err == nil {
			return info.IsDir() && info.Mode().Perm()&0200 != 0
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
	return false
}

func piPublicationEntry(env integrations.Env, root string, stat func(string) (os.FileInfo, error)) string {
	launcher := filepath.Join(filepath.Dir(root), "bin", "pi")
	home := absolute(env["HOME"])
	for _, brew := range []bool{false, true} {
		for _, dir := range filepath.SplitList(env["PATH"]) {
			if !filepath.IsAbs(dir) {
				continue
			}
			dir = absolute(dir)
			allowed := dir == filepath.Dir(launcher) || dir == filepath.Join(home, ".local", "bin") || dir == filepath.Join(home, "bin") || dir == filepath.Join(home, ".bin") || dir == filepath.Join(home, "local", "bin")
			if brew {
				allowed = isExecutable(filepath.Join(dir, "brew"), stat)
			}
			entry := filepath.Join(dir, "pi")
			_, err := os.Lstat(entry)
			if allowed && os.IsNotExist(err) && writableDirectory(dir, stat) {
				return entry
			}
		}
	}
	return launcher
}
