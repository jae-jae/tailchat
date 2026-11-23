package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

const (
	// DefaultRepo is the GitHub repository used for release checks and download links.
	DefaultRepo = "jae-jae/tailchat"
	// DefaultAPIBase is the default GitHub REST API base URL.
	DefaultAPIBase = "https://api.github.com"
)

// FallbackVersion is used when running an unpackaged binary where mygo.App.Version() is empty.
// Can be overridden at build time with -ldflags "-X tailchat/internal/updater.FallbackVersion=1.1.0".
var FallbackVersion = "1.0.0"

// Status describes the current auto-update state for UI rendering.
type Status struct {
	CurrentVersion   string
	Checking         bool
	Installing       bool
	Available        bool
	UpToDate         bool
	LatestVersion    string
	Notes            string
	ReleaseURL       string
	CanAutoInstall   bool
	ProgressDone     int64
	ProgressTotal    int64
	CheckedAt        time.Time
	LastError        string
	DismissedVersion string
}

// Progress returns [0, 1] download progress when installing.
func (s Status) Progress() float64 {
	if s.ProgressTotal <= 0 {
		return 0
	}
	p := float64(s.ProgressDone) / float64(s.ProgressTotal)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// BannerVisible reports whether the top update notification strip should be displayed.
func (s Status) BannerVisible() bool {
	return s.Available && s.LatestVersion != "" && s.DismissedVersion != s.LatestVersion
}

type nativeHandle interface {
	Version() string
	Notes() string
	Install(ctx context.Context, progress func(downloaded, total int64)) error
}

type mygoUpdateAdapter struct {
	up *mygo.Update
}

func (a mygoUpdateAdapter) Version() string { return a.up.Version }
func (a mygoUpdateAdapter) Notes() string   { return a.up.Notes }
func (a mygoUpdateAdapter) Install(ctx context.Context, progress func(downloaded, total int64)) error {
	return a.up.Install(ctx, progress)
}

type nativeBackend interface {
	Enabled() bool
	Check(ctx context.Context) (nativeHandle, error)
	AppVersion() string
	Relaunch()
}

type realNativeBackend struct{}

func (realNativeBackend) Enabled() bool { return mygo.Updater.Enabled() }
func (realNativeBackend) Check(ctx context.Context) (nativeHandle, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return nil, err
	}
	return mygoUpdateAdapter{up: up}, nil
}
func (realNativeBackend) AppVersion() string { return mygo.App.Version() }
func (realNativeBackend) Relaunch()          { mygo.App.Relaunch() }

// Options configures a Manager instance.
type Options struct {
	Repo       string
	APIBase    string
	HTTPClient *http.Client
	OnChange   func(Status)
	native     nativeBackend
}

// Manager coordinates background and manual update checks via MyGo's native signed updater,
// automatically falling back to the GitHub Releases API for unpackaged / dev binaries.
type Manager struct {
	repo     string
	apiBase  string
	client   *http.Client
	native   nativeBackend
	onChange func(Status)

	mu      sync.Mutex
	status  Status
	pending nativeHandle
}

// NewManager creates an update Manager.
func NewManager(opts Options) *Manager {
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = DefaultRepo
	}
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	native := opts.native
	if native == nil {
		native = realNativeBackend{}
	}
	curVer := resolveCurrentVersion(native.AppVersion())
	return &Manager{
		repo:     repo,
		apiBase:  apiBase,
		client:   client,
		native:   native,
		onChange: opts.OnChange,
		status: Status{
			CurrentVersion: curVer,
		},
	}
}

// SetOnChange registers a callback invoked whenever Status changes.
func (m *Manager) SetOnChange(fn func(Status)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// Status returns a snapshot of the current updater state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Dismiss hides the non-intrusive banner for the currently discovered version.
func (m *Manager) Dismiss() {
	m.mu.Lock()
	if m.status.LatestVersion != "" {
		m.status.DismissedVersion = m.status.LatestVersion
	}
	st := m.status
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// Check queries for a newer release. If manual is true, any previous dismissal is cleared
// and transient errors are surfaced in Status.LastError; silent background checks suppress
// network errors so offline startups stay quiet.
func (m *Manager) Check(ctx context.Context, manual bool) (Status, error) {
	m.mu.Lock()
	if m.status.Checking || m.status.Installing {
		st := m.status
		m.mu.Unlock()
		return st, nil
	}
	m.status.Checking = true
	if manual {
		m.status.LastError = ""
		m.status.DismissedVersion = ""
	}
	curVer := resolveCurrentVersion(m.native.AppVersion())
	m.status.CurrentVersion = curVer
	st := m.status
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb(st)
	}

	var (
		found      bool
		latestVer  string
		notes      string
		releaseURL string
		canInstall bool
		pending    nativeHandle
		checkErr   error
	)

	if m.native.Enabled() {
		up, err := m.native.Check(ctx)
		if err != nil {
			checkErr = err
		} else if up != nil {
			found = true
			latestVer = normalizeVersion(up.Version())
			notes = strings.TrimSpace(up.Notes())
			releaseURL = fmt.Sprintf("https://github.com/%s/releases/tag/v%s", m.repo, latestVer)
			canInstall = true
			pending = up
		}
	} else {
		rel, err := m.fetchLatestGitHubRelease(ctx, curVer)
		if err != nil {
			checkErr = err
		} else if rel != nil && CompareSemver(rel.Version, curVer) > 0 {
			found = true
			latestVer = rel.Version
			notes = rel.Notes
			releaseURL = rel.URL
			canInstall = false
		}
	}

	m.mu.Lock()
	m.status.Checking = false
	m.status.CheckedAt = time.Now()
	if checkErr != nil {
		if manual {
			m.status.LastError = checkErr.Error()
		}
	} else {
		m.status.LastError = ""
		m.status.Available = found
		m.status.UpToDate = !found
		m.status.LatestVersion = latestVer
		m.status.Notes = notes
		m.status.ReleaseURL = releaseURL
		m.status.CanAutoInstall = canInstall
		m.pending = pending
	}
	st = m.status
	cb = m.onChange
	m.mu.Unlock()

	if cb != nil {
		cb(st)
	}
	return st, checkErr
}

// InstallAndRelaunch downloads, verifies, installs the pending signed MyGo update,
// and relaunches the application.
func (m *Manager) InstallAndRelaunch(ctx context.Context) error {
	m.mu.Lock()
	pending := m.pending
	if pending == nil {
		m.mu.Unlock()
		return errors.New("no auto-installable update available")
	}
	if m.status.Installing {
		m.mu.Unlock()
		return nil
	}
	m.status.Installing = true
	m.status.LastError = ""
	m.status.ProgressDone = 0
	m.status.ProgressTotal = 0
	st := m.status
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb(st)
	}

	err := pending.Install(ctx, func(downloaded, total int64) {
		m.mu.Lock()
		m.status.ProgressDone = downloaded
		m.status.ProgressTotal = total
		cur := m.status
		fn := m.onChange
		m.mu.Unlock()
		if fn != nil {
			fn(cur)
		}
	})

	if err != nil {
		m.mu.Lock()
		m.status.Installing = false
		m.status.LastError = err.Error()
		cur := m.status
		fn := m.onChange
		m.mu.Unlock()
		if fn != nil {
			fn(cur)
		}
		return err
	}

	m.native.Relaunch()
	return nil
}

type githubReleaseInfo struct {
	Version string
	Notes   string
	URL     string
}

func (m *Manager) fetchLatestGitHubRelease(ctx context.Context, curVer string) (*githubReleaseInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", m.apiBase, m.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TailChat/"+curVer)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github releases status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		TagName    string `json:"tag_name"`
		Body       string `json:"body"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Draft || payload.Prerelease || strings.TrimSpace(payload.TagName) == "" {
		return nil, nil
	}
	ver := normalizeVersion(payload.TagName)
	htmlURL := strings.TrimSpace(payload.HTMLURL)
	if htmlURL == "" {
		htmlURL = fmt.Sprintf("https://github.com/%s/releases/tag/%s", m.repo, payload.TagName)
	}
	return &githubReleaseInfo{
		Version: ver,
		Notes:   strings.TrimSpace(payload.Body),
		URL:     htmlURL,
	}, nil
}

func resolveCurrentVersion(appVer string) string {
	v := normalizeVersion(appVer)
	if v == "" || v == "0.0.0" {
		return normalizeVersion(FallbackVersion)
	}
	return v
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

// CompareSemver compares two semantic version strings (with optional leading 'v').
// Returns 1 if a > b, -1 if a < b, and 0 if equal.
func CompareSemver(a, b string) int {
	pa, preA := parseSemver(a)
	pb, preB := parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return 1
		}
		if pa[i] < pb[i] {
			return -1
		}
	}
	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	case preA > preB:
		return 1
	case preA < preB:
		return -1
	default:
		return 0
	}
}

func parseSemver(v string) ([3]int, string) {
	v = normalizeVersion(v)
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		v = v[:idx]
	}
	pre := ""
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		pre = v[idx+1:]
		v = v[:idx]
	}
	var nums [3]int
	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err == nil && n >= 0 {
			nums[i] = n
		}
	}
	return nums, pre
}
