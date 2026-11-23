package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.1.0", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"1.1.0-beta.1", "1.1.0", -1},
		{"1.1.0", "1.1.0-beta.1", 1},
		{"1.1.0-beta.2", "1.1.0-beta.1", 1},
	}
	for _, tc := range cases {
		if got := CompareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

type fakeNativeHandle struct {
	version    string
	notes      string
	installErr error
	installed  atomic.Bool
}

func (f *fakeNativeHandle) Version() string { return f.version }
func (f *fakeNativeHandle) Notes() string   { return f.notes }
func (f *fakeNativeHandle) Install(_ context.Context, progress func(downloaded, total int64)) error {
	if progress != nil {
		progress(50, 100)
		progress(100, 100)
	}
	if f.installErr != nil {
		return f.installErr
	}
	f.installed.Store(true)
	return nil
}

type fakeNativeBackend struct {
	enabled    bool
	appVersion string
	handle     nativeHandle
	checkErr   error
	relaunched atomic.Bool
}

func (f *fakeNativeBackend) Enabled() bool { return f.enabled }
func (f *fakeNativeBackend) Check(context.Context) (nativeHandle, error) {
	return f.handle, f.checkErr
}
func (f *fakeNativeBackend) AppVersion() string { return f.appVersion }
func (f *fakeNativeBackend) Relaunch()          { f.relaunched.Store(true) }

func TestManagerNativeAutoInstall(t *testing.T) {
	handle := &fakeNativeHandle{
		version: "1.2.0",
		notes:   "## Highlights\n- Inline image previews",
	}
	backend := &fakeNativeBackend{
		enabled:    true,
		appVersion: "1.0.0",
		handle:     handle,
	}
	mgr := NewManager(Options{
		Repo:   "jae-jae/tailchat",
		native: backend,
	})

	st, err := mgr.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !st.Available || !st.CanAutoInstall || st.LatestVersion != "1.2.0" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if !st.BannerVisible() {
		t.Fatalf("expected BannerVisible true")
	}

	if err := mgr.InstallAndRelaunch(context.Background()); err != nil {
		t.Fatalf("InstallAndRelaunch failed: %v", err)
	}
	if !handle.installed.Load() || !backend.relaunched.Load() {
		t.Fatalf("expected install and relaunch to be invoked")
	}
}

func TestManagerGitHubFallbackCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/jae-jae/tailchat/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.1.0",
			"body": "Media preview & auto-update",
			"html_url": "https://github.com/jae-jae/tailchat/releases/tag/v1.1.0",
			"draft": false,
			"prerelease": false
		}`))
	}))
	defer srv.Close()

	backend := &fakeNativeBackend{
		enabled:    false,
		appVersion: "1.0.0",
	}
	mgr := NewManager(Options{
		Repo:       "jae-jae/tailchat",
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		native:     backend,
	})

	st, err := mgr.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("fallback Check failed: %v", err)
	}
	if !st.Available || st.CanAutoInstall || st.LatestVersion != "1.1.0" {
		t.Fatalf("unexpected fallback status: %+v", st)
	}
	if st.ReleaseURL != "https://github.com/jae-jae/tailchat/releases/tag/v1.1.0" {
		t.Fatalf("unexpected release URL: %q", st.ReleaseURL)
	}

	mgr.Dismiss()
	if mgr.Status().BannerVisible() {
		t.Fatalf("expected BannerVisible false after Dismiss")
	}
}
