//go:build !windows

package ui

import (
	"os/exec"
	"runtime"
	"strings"
)

// detectPlatformLocale queries OS-native locale preferences when POSIX
// environment variables (LC_ALL/LANG/etc.) are unset or neutral.
func detectPlatformLocale() string {
	// macOS GUI apps launched from Finder/Dock often have empty LANG/LC_* env vars.
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			if first := extractFirstAppleLanguage(string(out)); first != "" {
				return first
			}
		}
		if out, err := exec.Command("defaults", "read", "-g", "AppleLocale").Output(); err == nil {
			if raw := strings.TrimSpace(string(out)); raw != "" {
				return raw
			}
		}
	}
	return ""
}
