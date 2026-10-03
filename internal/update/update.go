package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/deLiseLINO/prism/internal/buildinfo"
)

type Method string

const (
	MethodUnknown Method = "unknown"
	MethodBrew    Method = "brew"
	MethodGo      Method = "go"
)

const (
	RefreshInterval       = 5 * time.Minute
	latestReleaseURL      = "https://api.github.com/repos/deLiseLINO/prism/releases/latest"
	homebrewFormulaURL    = "https://raw.githubusercontent.com/deLiseLINO/homebrew-tap/main/Formula/prism.rb"
	releasesPageURL       = "https://github.com/deLiseLINO/prism/releases"
	goInstallTarget       = "github.com/deLiseLINO/prism/cmd/prism@latest"
	goInstallTargetPrefix = "github.com/deLiseLINO/prism/cmd/prism@"
	homebrewUpgradeTarget = "deLiseLINO/tap/prism"
	requestTimeout        = 10 * time.Second
	userAgent             = "prism/update-check"
)

var formulaVersionPattern = regexp.MustCompile(`(?m)^\s*version\s+"([^"]+)"`)

func DetectMethod() Method {
	exePath, err := os.Executable()
	if err != nil {
		return MethodUnknown
	}
	home, _ := os.UserHomeDir()
	gopath := filepath.SplitList(os.Getenv("GOPATH"))
	for i, entry := range gopath {
		gopath[i] = resolveLink(entry)
	}
	return DetectMethodFromInputs(
		resolveLink(exePath),
		resolveLink(os.Getenv("GOBIN")),
		strings.Join(gopath, string(filepath.ListSeparator)),
		resolveLink(home),
	)
}

func resolveLink(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func DetectMethodFromInputs(exePath, gobin, gopath, home string) Method {
	exePath = strings.TrimSpace(exePath)
	if exePath == "" {
		return MethodUnknown
	}
	exePath = filepath.Clean(exePath)
	if exePath == "." {
		return MethodUnknown
	}

	if goBinDir := resolveGoBinDir(gobin, gopath, home); goBinDir != "" && filepath.Dir(exePath) == goBinDir {
		return MethodGo
	}

	for _, prefix := range []string{
		"/opt/homebrew",
		"/usr/local",
		"/home/linuxbrew/.linuxbrew",
	} {
		if hasPathPrefix(exePath, prefix) {
			return MethodBrew
		}
	}
	return MethodUnknown
}

func resolveGoBinDir(gobin, gopath, home string) string {
	if gobin = strings.TrimSpace(gobin); gobin != "" {
		return filepath.Clean(gobin)
	}
	for _, entry := range filepath.SplitList(strings.TrimSpace(gopath)) {
		if entry = strings.TrimSpace(entry); entry != "" {
			return filepath.Join(filepath.Clean(entry), "bin")
		}
	}
	if home = strings.TrimSpace(home); home == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), "go", "bin")
}

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

func SupportsAutoUpdate(method Method) bool {
	return method == MethodBrew || method == MethodGo
}

func ShouldRefresh(state State, now time.Time) bool {
	return state.LastCheckedAt.IsZero() || state.LastCheckedAt.Before(now.Add(-RefreshInterval))
}

func ShouldPrompt(enabled bool, state State, currentVersion string, method Method) (string, bool) {
	if !enabled || !SupportsAutoUpdate(method) {
		return "", false
	}
	latest := strings.TrimPrefix(strings.TrimSpace(state.LatestVersion), "v")
	if latest == "" || !IsNewer(latest, currentVersion) {
		return "", false
	}
	if strings.TrimPrefix(strings.TrimSpace(state.DismissedVersion), "v") == latest {
		return "", false
	}
	return latest, true
}

type Source struct {
	HTTP       *http.Client
	FormulaURL string
	ReleaseURL string
}

func DefaultSource() Source {
	return Source{
		HTTP:       &http.Client{Timeout: requestTimeout},
		FormulaURL: homebrewFormulaURL,
		ReleaseURL: latestReleaseURL,
	}
}

func (s Source) Refresh(ctx context.Context, state State, method Method) (State, error) {
	latest, err := s.FetchLatest(ctx, method)
	if err != nil {
		return state, err
	}
	state.LatestVersion = latest
	state.LastCheckedAt = time.Now().UTC()
	return state, nil
}

func (s Source) FetchLatest(ctx context.Context, method Method) (string, error) {
	if method == MethodBrew {
		if version, err := s.fetchFormulaVersion(ctx); err == nil {
			return version, nil
		}
	}
	return s.fetchReleaseVersion(ctx)
}

func (s Source) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func (s Source) fetchFormulaVersion(ctx context.Context) (string, error) {
	resp, err := s.get(ctx, s.FormulaURL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("formula request failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	matches := formulaVersionPattern.FindStringSubmatch(string(body))
	if len(matches) != 2 {
		return "", fmt.Errorf("failed to parse formula version")
	}
	version := strings.TrimPrefix(strings.TrimSpace(matches[1]), "v")
	if !semver.IsValid(normalize(version)) {
		return "", fmt.Errorf("formula version %q is not valid", version)
	}
	return version, nil
}

func (s Source) fetchReleaseVersion(ctx context.Context) (string, error) {
	resp, err := s.get(ctx, s.ReleaseURL, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release request failed: %s", resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	version := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if version == "" {
		return "", fmt.Errorf("latest release tag is empty")
	}
	return version, nil
}

func normalize(version string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func IsNewer(latest, current string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	if current == buildinfo.DevVersion {
		return false
	}
	latest, current = normalize(latest), normalize(current)
	if !semver.IsValid(latest) || !semver.IsValid(current) {
		return false
	}
	return semver.Compare(latest, current) > 0
}

func Command(method Method, latestVersion string) (string, []string, bool) {
	switch method {
	case MethodBrew:
		return "brew", []string{"upgrade", homebrewUpgradeTarget}, true
	case MethodGo:
		return "go", []string{"install", goInstallTargetForVersion(latestVersion)}, true
	default:
		return "", nil, false
	}
}

func CommandString(method Method, latestVersion string) string {
	command, args, ok := Command(method, latestVersion)
	if !ok {
		return ""
	}
	return strings.Join(append([]string{command}, args...), " ")
}

func ReleaseNotesURL(version string) string {
	if !semver.IsValid(normalize(version)) {
		return releasesPageURL + "/latest"
	}
	return releasesPageURL + "/tag/" + normalize(version)
}

func RunUpgrade(method Method, latestVersion string, stdout, stderr io.Writer) error {
	command, args, ok := Command(method, latestVersion)
	if !ok {
		return fmt.Errorf("unsupported update method: %s", method)
	}

	fmt.Fprintf(stdout, "Updating prism via `%s`...\n", CommandString(method, latestVersion))
	cmd := exec.Command(command, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Update completed. The next `prism` run restarts the background daemon on the new version.")
	return nil
}

func goInstallTargetForVersion(version string) string {
	if !semver.IsValid(normalize(version)) {
		return goInstallTarget
	}
	return goInstallTargetPrefix + normalize(version)
}

func ManualUpgradeInstructions(currentVersion, latestVersion string) string {
	lines := []string{fmt.Sprintf("Current version: %s", strings.TrimSpace(currentVersion))}
	if latestVersion = strings.TrimSpace(latestVersion); latestVersion != "" {
		lines = append(lines, fmt.Sprintf("Latest version: %s", latestVersion))
	}
	lines = append(lines,
		"Automatic upgrade is unavailable because the installation method could not be determined.",
		"Manual update options:",
		fmt.Sprintf("  brew upgrade %s", homebrewUpgradeTarget),
		fmt.Sprintf("  go install %s", goInstallTargetForVersion(latestVersion)),
		fmt.Sprintf("  Releases: %s", ReleaseNotesURL(latestVersion)),
	)
	return strings.Join(lines, "\n")
}
