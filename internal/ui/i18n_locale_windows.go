//go:build windows

package ui

import (
	"strings"
	"syscall"
	"unsafe"
)

const (
	muiLanguageName     = 0x08
	localeNameMaxLength = 85
)

var (
	kernel32DLL                    = syscall.NewLazyDLL("kernel32.dll")
	procGetUserPreferredUILangs    = kernel32DLL.NewProc("GetUserPreferredUILanguages")
	procGetUserDefaultLocaleName   = kernel32DLL.NewProc("GetUserDefaultLocaleName")
	procGetSystemDefaultLocaleName = kernel32DLL.NewProc("GetSystemDefaultLocaleName")
	procGetUserDefaultUILanguage   = kernel32DLL.NewProc("GetUserDefaultUILanguage")
)

// detectPlatformLocale queries Windows native UI language and locale APIs
// without spawning subprocesses (avoiding console window flicker in GUI apps).
func detectPlatformLocale() string {
	if tag := winUserPreferredUILanguage(); tag != "" {
		return tag
	}
	if tag := winLocaleName(procGetUserDefaultLocaleName); tag != "" {
		return tag
	}
	if tag := winLocaleName(procGetSystemDefaultLocaleName); tag != "" {
		return tag
	}
	if procGetUserDefaultUILanguage.Find() == nil {
		r1, _, _ := procGetUserDefaultUILanguage.Call()
		if tag := langIDToLocaleTag(uint16(r1)); tag != "" {
			return tag
		}
	}
	return ""
}

func winUserPreferredUILanguage() string {
	if procGetUserPreferredUILangs.Find() != nil {
		return ""
	}
	var numLangs uint32
	var bufSize uint32
	r1, _, _ := procGetUserPreferredUILangs.Call(
		uintptr(muiLanguageName),
		uintptr(unsafe.Pointer(&numLangs)),
		0,
		uintptr(unsafe.Pointer(&bufSize)),
	)
	if r1 == 0 || bufSize == 0 {
		return ""
	}
	buf := make([]uint16, bufSize)
	r1, _, _ = procGetUserPreferredUILangs.Call(
		uintptr(muiLanguageName),
		uintptr(unsafe.Pointer(&numLangs)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bufSize)),
	)
	if r1 == 0 {
		return ""
	}
	return extractFirstUTF16MultiString(buf)
}

func winLocaleName(proc *syscall.LazyProc) string {
	if proc.Find() != nil {
		return ""
	}
	buf := make([]uint16, localeNameMaxLength)
	r1, _, _ := proc.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r1 == 0 {
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(buf))
}
