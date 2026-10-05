//go:build darwin || linux

package agentinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const archiveSizeCap int64 = 1 << 30
const extractedSizeCap int64 = 2 << 30

type codexManifest struct {
	LayoutVersion int    `json:"layoutVersion"`
	Version       string `json:"version"`
	Target        string `json:"target"`
	Variant       string `json:"variant"`
	Entrypoint    string `json:"entrypoint"`
	ResourcesDir  string `json:"resourcesDir"`
	PathDir       string `json:"pathDir"`
}
type archiveRelease struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func safeComponent(value string) bool {
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r)) {
			return false
		}
	}
	return true
}
func readCodexManifest(dir string) (codexManifest, error) {
	var manifest codexManifest
	path := filepath.Join(dir, "codex-package.json")
	info, err := os.Lstat(path)
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return manifest, fmt.Errorf("invalid package manifest file")
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err != nil {
		return manifest, err
	}
	if len(data) > 256<<10 {
		return manifest, fmt.Errorf("package manifest exceeds limit")
	}
	err = json.Unmarshal(data, &manifest)
	return manifest, err
}
func validateCodexPackage(dir, version, target string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("release directory is not a regular directory")
	}
	manifest, err := readCodexManifest(dir)
	if err != nil {
		return err
	}
	if manifest.LayoutVersion != 1 || manifest.Variant != "codex" || manifest.Version != version || manifest.Target != target || !safeComponent(version) || normalizedVersion(version) == "" || !safeComponent(target) || manifest.Entrypoint != "bin/codex" || manifest.ResourcesDir != "codex-resources" || manifest.PathDir != "codex-path" {
		return fmt.Errorf("package metadata did not match the requested release")
	}
	for _, name := range []string{manifest.Entrypoint, manifest.ResourcesDir, manifest.PathDir} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || !inside(absolute(real), absolute(dir)) {
			return fmt.Errorf("package path escaped release directory")
		}
		if name == manifest.Entrypoint {
			if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
				return fmt.Errorf("package entrypoint is not a regular executable")
			}
		} else if !info.IsDir() {
			return fmt.Errorf("package resources are not directories")
		}
	}
	return nil
}
func recognizeCodexStandalone(entry, real string) (installTarget, bool) {
	release := filepath.Dir(filepath.Dir(real))
	releases := filepath.Dir(release)
	root := filepath.Dir(releases)
	if filepath.Base(real) != "codex" || filepath.Base(filepath.Dir(real)) != "bin" || filepath.Base(releases) != "releases" {
		return installTarget{}, false
	}
	active, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || absolute(active) != release {
		return installTarget{}, false
	}
	manifest, err := readCodexManifest(release)
	if err != nil || validateCodexPackage(release, manifest.Version, manifest.Target) != nil {
		return installTarget{}, false
	}
	currentEntry := filepath.Join(root, "current", "bin", "codex")
	path := entry
	follows := false
	for i := 0; i < 32; i++ {
		if path == currentEntry {
			follows = true
			break
		}
		link, err := os.Readlink(path)
		if err != nil {
			break
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		path = filepath.Clean(link)
	}
	if !follows {
		return installTarget{}, false
	}
	return installTarget{source: SourceScript, entry: entry, root: root, name: "codex-standalone", archiveTarget: manifest.Target}, true
}
func (m *Manager) codexAsset(ctx context.Context, version, target string) (archiveRelease, error) {
	var release struct {
		Tag    string           `json:"tag_name"`
		Assets []archiveRelease `json:"assets"`
	}
	err := m.releaseJSON(ctx, "https://api.github.com/repos/openai/codex/releases/tags/rust-v"+url.PathEscape(version), &release)
	if err != nil {
		return archiveRelease{}, err
	}
	if release.Tag != "rust-v"+version {
		return archiveRelease{}, fmt.Errorf("archive release tag does not match frozen version")
	}
	name := "codex-package-" + target + ".tar.gz"
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		u, err := url.Parse(asset.URL)
		digest, digestErr := hex.DecodeString(strings.TrimPrefix(asset.Digest, "sha256:"))
		if err != nil || u.Scheme != "https" || u.Hostname() != "github.com" || u.User != nil || u.Port() != "" || u.Path != "/openai/codex/releases/download/rust-v"+version+"/"+name || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(asset.Digest, "sha256:") || digestErr != nil || len(digest) != sha256.Size || asset.Size <= 0 || asset.Size > archiveSizeCap {
			return archiveRelease{}, fmt.Errorf("archive release metadata is invalid")
		}
		return asset, nil
	}
	return archiveRelease{}, fmt.Errorf("release has no archive for selected target %s", target)
}
func (m *Manager) downloadArchive(ctx context.Context, asset archiveRelease, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	client := *m.http
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		host := req.URL.Hostname()
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || (host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
			return fmt.Errorf("untrusted archive redirect")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("archive download returned %s", resp.Status)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, asset.Size+1))
	if err != nil {
		return err
	}
	if n != asset.Size {
		return fmt.Errorf("archive download size %d does not match %d", n, asset.Size)
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(asset.Digest, "sha256:") {
		return fmt.Errorf("archive failed SHA-256 verification")
	}
	return file.Sync()
}
func extractCodexArchive(ctx context.Context, archive, stage string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if count >= 10000 {
			return fmt.Errorf("archive has too many entries")
		}
		name := filepath.FromSlash(header.Name)
		clean := filepath.Clean(name)
		if clean == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		path := filepath.Join(stage, clean)
		if filepath.IsAbs(name) || !inside(path, stage) || clean != strings.TrimSuffix(name, string(filepath.Separator)) || strings.Contains(name, "\\") {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		if header.Size < 0 || header.Size > extractedSizeCap-total {
			return fmt.Errorf("archive exceeds extracted size limit")
		}
		total += header.Size
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			syncErr := output.Sync()
			closeErr := output.Close()
			if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive contains unsupported link or special file")
		}
	}
	padding, err := io.ReadAll(io.LimitReader(gz, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(padding) > 64<<10 {
		return fmt.Errorf("archive contains excessive trailing padding")
	}
	for _, value := range padding {
		if value != 0 {
			return fmt.Errorf("archive contains trailing unpacked data")
		}
	}
	return nil
}
func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
func (m *Manager) updateStandalone(ctx context.Context, def Definition, target installTarget, check releaseCheck, job *Job) error {
	if _, err := os.Lstat(filepath.Join(target.root, "install.lock.d")); err == nil {
		return fmt.Errorf("another installation is in progress")
	}
	lockPath := filepath.Join(target.root, "install.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("installation lock is not a regular file")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("installation lock is busy: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if obs := m.observeEntry(def, target.entry); obs.reason != "" || obs.target != target {
		return fmt.Errorf("standalone owner changed before mutation")
	}
	if real, err := m.eval(target.entry); err != nil || absolute(real) != check.entryReal {
		return fmt.Errorf("active standalone release changed before mutation")
	}
	asset, err := m.codexAsset(ctx, check.expected, target.archiveTarget)
	if err != nil {
		return err
	}
	attempt, err := os.MkdirTemp(filepath.Join(target.root, "releases"), ".prism-partial-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(attempt)
	archive := filepath.Join(attempt, "archive.tar.gz")
	stage := filepath.Join(attempt, "payload")
	if err := os.Mkdir(stage, 0755); err != nil {
		return err
	}
	if err := m.downloadArchive(ctx, asset, archive); err != nil {
		return err
	}
	if err := extractCodexArchive(ctx, archive, stage); err != nil {
		return err
	}
	if err := validateCodexPackage(stage, check.expected, target.archiveTarget); err != nil {
		return err
	}
	staged, _, err := m.versionAt(ctx, def, filepath.Join(stage, "bin", "codex"), m.verifyTimeout)
	if err != nil {
		return err
	}
	if compareVersions(staged, check.expected) != 0 {
		return fmt.Errorf("staged version %s differs from expected %s", staged, check.expected)
	}
	destination := filepath.Join(target.root, "releases", check.expected+"-"+target.archiveTarget)
	if _, err := os.Lstat(destination); err == nil {
		if err := validateCodexPackage(destination, check.expected, target.archiveTarget); err != nil {
			return err
		}
		version, _, err := m.versionAt(ctx, def, filepath.Join(destination, "bin", "codex"), m.verifyTimeout)
		if err != nil || compareVersions(version, check.expected) != 0 {
			return fmt.Errorf("existing release failed version verification")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		if err := os.Rename(stage, destination); err != nil {
			return err
		}
		if err := syncDir(filepath.Join(target.root, "releases")); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if obs := m.observeEntry(def, target.entry); obs.reason != "" || obs.target != target {
		return fmt.Errorf("standalone owner changed before activation")
	}
	if real, err := m.eval(target.entry); err != nil || absolute(real) != check.entryReal {
		return fmt.Errorf("active standalone release changed before activation")
	}
	link := filepath.Join(target.root, ".current-"+filepath.Base(attempt))
	defer os.Remove(link)
	if err := os.Symlink(destination, link); err != nil {
		return err
	}
	if err := os.Rename(link, filepath.Join(target.root, "current")); err != nil {
		return err
	}
	if err := syncDir(target.root); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.verifyRetry)
	defer cancel()
	version, err := m.verifyVersion(verifyCtx, def, target)
	m.evidence(job, check, version)
	if err == nil && compareVersions(version, check.expected) < 0 {
		return fmt.Errorf("activated version %s is older than expected %s", version, check.expected)
	}
	return err
}
