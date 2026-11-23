package ui

import (
	"path/filepath"
	"strings"

	mygoui "github.com/egoist/mygo/ui"
)

// Zen Popover (Scheme C) color tokens derived from tokens.css.
var (
	zenBg            = mygoui.Hex("#F3F5F9")
	zenSurface       = mygoui.Hex("#FFFFFF")
	zenSurfaceSubtle = mygoui.Hex("#F8FAFC")
	zenSurfaceHover  = mygoui.Hex("#EEF2F7")
	zenInputFill     = mygoui.Hex("#F1F5F9")

	zenBorder       = mygoui.RGBA(15, 23, 42, 0.08)
	zenBorderStrong = mygoui.RGBA(15, 23, 42, 0.14)

	zenInk          = mygoui.Hex("#0F172A")
	zenInkSecondary = mygoui.Hex("#475569")
	zenInkMuted     = mygoui.Hex("#94A3B8")

	zenSelfBubbleBg  = mygoui.Hex("#0F172A")
	zenSelfBubbleInk = mygoui.Hex("#FFFFFF")

	zenAccent        = mygoui.Hex("#0A84FF")
	zenAccentHover   = mygoui.Hex("#0070E0")
	zenAccentPressed = mygoui.Hex("#005BBF")
	zenAccentSoft    = mygoui.RGBA(10, 132, 255, 0.11)
	zenAccentBorder  = mygoui.RGBA(10, 132, 255, 0.28)

	zenDirect     = mygoui.Hex("#10B981")
	zenDirectText = mygoui.Hex("#059669")
	zenDirectSoft = mygoui.RGBA(16, 185, 129, 0.12)

	zenDERP     = mygoui.Hex("#F59E0B")
	zenDERPText = mygoui.Hex("#D97706")
	zenDERPSoft = mygoui.RGBA(245, 158, 11, 0.14)

	zenOffline    = mygoui.Hex("#94A3B8")
	zenDanger     = mygoui.Hex("#EF4444")
	zenDangerDark = mygoui.Hex("#DC2626")
	zenDangerSoft = mygoui.RGBA(239, 68, 68, 0.10)

	zenProgressTrack = mygoui.Hex("#E2E8F0")
	zenTimeChipBg    = mygoui.RGBA(15, 23, 42, 0.05)
	zenDropHintBg    = mygoui.RGBA(255, 255, 255, 0.60)
)

var cachedZenTheme = buildZenTheme()

func buildZenTheme() *mygoui.Theme {
	t := *mygoui.LightTheme()
	t.Dark = false
	t.Background = zenBg
	t.Surface = zenSurface
	t.SurfaceHover = zenSurfaceSubtle
	t.SurfacePressed = zenInputFill
	t.Border = zenBorderStrong
	t.Text = zenInk
	t.TextMuted = zenInkMuted
	t.Accent = zenAccent
	t.AccentHover = zenAccentHover
	t.AccentPressed = zenAccentPressed
	t.AccentText = zenSelfBubbleInk
	t.InactiveAccent = zenAccent
	t.Danger = zenDanger
	t.Warning = zenDERP
	t.Success = zenDirect
	t.Selection = zenAccent.Alpha(0.22)
	t.Focus = zenAccent.Alpha(0.45)
	t.Inverse = zenInk
	t.InverseText = zenSelfBubbleInk
	t.Scrollbar = mygoui.RGBA(15, 23, 42, 0.20)
	t.ScrollbarWidth = 5
	t.Radius = 10
	t.Spacing = 4
	t.FontSize = 13
	return &t
}

// zenTheme returns the Scheme C · Zen Popover theme for mygo/ui.
func zenTheme() *mygoui.Theme {
	return cachedZenTheme
}

// Crisp 24x24 Lucide-style inline SVG icons parsed once at package initialization.
var (
	iconSearch = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>`))

	iconSmartphone = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="20" x="5" y="2" rx="2" ry="2"/><path d="M12 18h.01"/></svg>`))

	iconSettings = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`))

	iconPlus = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="M12 5v14"/></svg>`))

	iconArrowUp = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 7-7 7 7"/><path d="M12 19V5"/></svg>`))

	iconClipboard = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="8" height="4" x="8" y="2" rx="1" ry="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/></svg>`))

	iconCopy = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`))

	iconTrash = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/></svg>`))

	iconCheckCircle = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/></svg>`))

	iconCircleDashed = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.1 2.182a10 10 0 0 1 3.8 0"/><path d="M13.9 21.818a10 10 0 0 1-3.8 0"/><path d="M17.609 3.721a10 10 0 0 1 2.69 2.7"/><path d="M2.182 13.9a10 10 0 0 1 0-3.8"/><path d="M20.279 17.609a10 10 0 0 1-2.7 2.69"/><path d="M21.818 10.1a10 10 0 0 1 0 3.8"/><path d="M3.721 6.391a10 10 0 0 1 2.7-2.69"/><path d="M6.391 20.279a10 10 0 0 1-2.69-2.7"/></svg>`))

	iconX = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`))

	iconLock = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>`))

	iconServer = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/></svg>`))

	iconLaptop = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 16V7a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v9m16 0H4m16 0 1.28 2.55a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45L4 16"/></svg>`))

	iconDownload = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/></svg>`))

	iconPause = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="14" y="4" width="4" height="16" rx="1"/><rect x="6" y="4" width="4" height="16" rx="1"/></svg>`))

	iconPlay = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="6 3 20 12 6 21 6 3"/></svg>`))

	iconExternalLink = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/></svg>`))

	iconQRCode = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="5" height="5" x="3" y="3" rx="1"/><rect width="5" height="5" x="16" y="3" rx="1"/><rect width="5" height="5" x="3" y="16" rx="1"/><path d="M21 16h-3a2 2 0 0 0-2 2v3"/><path d="M21 21v.01"/><path d="M12 7v3a2 2 0 0 1-2 2H7"/><path d="M3 12h.01"/><path d="M12 3h.01"/><path d="M12 16v.01"/><path d="M16 12h1"/><path d="M21 12v.01"/><path d="M12 21v-1"/></svg>`))

	iconRefreshCw = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/></svg>`))

	iconLink = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>`))

	iconCheck = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`))

	iconImage = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/></svg>`))

	iconVideo = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m16 13 5.223 3.482a.5.5 0 0 0 .777-.416V7.87a.5.5 0 0 0-.752-.432L16 10.5"/><rect x="2" y="6" width="14" height="12" rx="2"/></svg>`))

	iconMusic = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg>`))

	iconSparkles = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 9.27 9.27 3 12l6.27 2.73L12 21l2.73-6.27L21 12l-6.27-2.73Z"/></svg>`))

	iconFolderOpen = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/></svg>`))

	iconInfo = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>`))

	iconGlobe = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/></svg>`))

	iconGitHub = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 22v-4a4.8 4.8 0 0 0-1-3.5c3 0 6-2 6-5.5.08-1.25-.27-2.48-1-3.5.28-1.15.28-2.35 0-3.5 0 0-1 0-3 1.5-2.64-.5-5.36-.5-8 0C6 2 5 2 5 2c-.3 1.15-.3 2.35 0 3.5A5.403 5.403 0 0 0 4 9c0 3.5 3 5.5 6 5.5-.39.49-.68 1.05-.85 1.65-.17.6-.22 1.23-.15 1.85v4"/><path d="M9 18c-4.51 2-5-2-7-2"/></svg>`))

	iconAlertTriangle = mygoui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>`))
)

// zenIcons lists all embedded SVG icons for unit-test validation.
var zenIcons = map[string]*mygoui.SVG{
	"search":        iconSearch,
	"smartphone":    iconSmartphone,
	"settings":      iconSettings,
	"plus":          iconPlus,
	"arrowUp":       iconArrowUp,
	"clipboard":     iconClipboard,
	"copy":          iconCopy,
	"trash":         iconTrash,
	"checkCircle":   iconCheckCircle,
	"circleDashed":  iconCircleDashed,
	"x":             iconX,
	"lock":          iconLock,
	"server":        iconServer,
	"laptop":        iconLaptop,
	"download":      iconDownload,
	"pause":         iconPause,
	"play":          iconPlay,
	"externalLink":  iconExternalLink,
	"qrCode":        iconQRCode,
	"refreshCw":     iconRefreshCw,
	"link":          iconLink,
	"check":         iconCheck,
	"image":         iconImage,
	"video":         iconVideo,
	"music":         iconMusic,
	"sparkles":      iconSparkles,
	"folderOpen":    iconFolderOpen,
	"info":          iconInfo,
	"globe":         iconGlobe,
	"github":        iconGitHub,
	"alertTriangle": iconAlertTriangle,
}

// zenCircleBtn renders a 32x32 circular icon button with Zen Popover styling.
func zenCircleBtn(c *mygoui.Context, key string, svg *mygoui.SVG, label string, active bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := zenSurface
	border := zenBorder
	fg := zenInkSecondary

	if active {
		bg = zenAccentSoft
		border = zenAccentBorder
		fg = zenAccent
		if btn.Hovered() {
			bg = mygoui.RGBA(10, 132, 255, 0.16)
		}
	} else {
		if btn.Pressed() {
			bg = zenInputFill
			fg = zenInk
		} else if btn.Hovered() {
			bg = zenSurfaceSubtle
			fg = zenInk
		}
	}

	btn.Size(32, 32).
		Radius(99).
		Background(bg).
		Border(1, border).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Cursor(mygoui.CursorPointer).
		Label(label).
		Tooltip(label).
		Children(func() {
			mygoui.Icon(c, svg).Size(15, 15).TextColor(fg)
		})
	return btn
}

// zenCapsuleIconBtn renders a borderless circular icon button inside the Floating Capsule composer.
func zenCapsuleIconBtn(c *mygoui.Context, key string, svg *mygoui.SVG, label string, size, iconSize float32, muted, active bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := mygoui.RGBA(0, 0, 0, 0)
	fg := zenInkSecondary
	if muted {
		fg = zenInkMuted
	}

	if active {
		bg = zenAccentSoft
		fg = zenAccent
		if btn.Hovered() {
			bg = mygoui.RGBA(10, 132, 255, 0.17)
		}
	} else {
		if btn.Pressed() {
			bg = zenProgressTrack
			fg = zenInk
		} else if btn.Hovered() {
			bg = zenInputFill
			fg = zenInk
		}
	}

	btn.Size(size, size).
		Radius(99).
		Background(bg).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Shrink(0).
		Cursor(mygoui.CursorPointer).
		Label(label).
		Tooltip(label).
		Children(func() {
			mygoui.Icon(c, svg).Size(iconSize, iconSize).TextColor(fg)
		})
	return btn
}

// zenPairHeaderBtn renders the dedicated top-right Pair CTA pill button.
func zenPairHeaderBtn(c *mygoui.Context, label, tooltip string, active bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key("btn-pair"))

	bg := zenAccentSoft
	border := zenAccent.Alpha(0.24)
	fg := zenAccent

	if active {
		bg = zenAccent
		border = zenAccent
		fg = zenSelfBubbleInk
		if btn.Pressed() {
			bg = zenAccentPressed
			border = zenAccentPressed
		} else if btn.Hovered() {
			bg = zenAccentHover
			border = zenAccentHover
		}
	} else {
		if btn.Pressed() {
			bg = mygoui.RGBA(10, 132, 255, 0.22)
			border = zenAccent.Alpha(0.42)
		} else if btn.Hovered() {
			bg = mygoui.RGBA(10, 132, 255, 0.17)
			border = zenAccent.Alpha(0.36)
		}
	}

	btn.Height(32).
		Padding(0, 12).
		Radius(99).
		Gap(5).
		Background(bg).
		Border(1, border).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Cursor(mygoui.CursorPointer).
		Label(label).
		Tooltip(tooltip).
		Children(func() {
			mygoui.Icon(c, iconQRCode).Size(14, 14).TextColor(fg)
			mygoui.Text(c, label).FontSize(12.5).Bold().TextColor(fg).NoWrap()
		})
	return btn
}

// zenPillBtn renders a rounded pill CTA button in primary blue or secondary surface style.
func zenPillBtn(c *mygoui.Context, key, text string, primary, disabled bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := zenSurfaceSubtle
	border := zenBorder
	fg := zenInk

	if primary {
		bg = zenAccent
		border = zenAccent
		fg = zenSelfBubbleInk
		if !disabled {
			if btn.Pressed() {
				bg = zenAccentPressed
				border = zenAccentPressed
			} else if btn.Hovered() {
				bg = zenAccentHover
				border = zenAccentHover
			}
		}
	} else if !disabled {
		if btn.Pressed() {
			bg = zenProgressTrack
		} else if btn.Hovered() {
			bg = zenSurfaceHover
		}
	}

	btn.Padding(5, 12).
		Radius(99).
		Background(bg).
		Border(1, border).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Shrink(0).
		Disabled(disabled).
		Label(text)
	if !disabled {
		btn.Cursor(mygoui.CursorPointer)
	}
	btn.Children(func() {
		mygoui.Text(c, text).FontSize(12).FontWeight(550).TextColor(fg).NoWrap()
	})
	return btn
}

// zenMicroBtn renders a low-contrast compact ghost trigger beneath chat bubbles.
func zenMicroBtn(c *mygoui.Context, key string, svg *mygoui.SVG, text string, danger bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := mygoui.RGBA(0, 0, 0, 0)
	fg := zenInkMuted
	if btn.Hovered() {
		if danger {
			bg = zenDangerSoft
			fg = zenDanger
		} else {
			bg = zenTimeChipBg
			fg = zenInkSecondary
		}
	}

	btn.Padding(2, 7).
		Gap(4).
		Radius(99).
		Background(bg).
		AlignItems(mygoui.Center).
		Shrink(0).
		Cursor(mygoui.CursorPointer).
		Label(text).
		Children(func() {
			if svg != nil {
				mygoui.Icon(c, svg).Size(11, 11).TextColor(fg)
			}
			mygoui.Text(c, text).FontSize(11).TextColor(fg).NoWrap()
		})
	return btn
}

// zenDangerPillBtn renders a compact red-tinted pill button for destructive actions like revoking a device.
func zenDangerPillBtn(c *mygoui.Context, key, text string) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := zenDangerSoft
	border := mygoui.RGBA(239, 68, 68, 0.22)
	fg := zenDanger
	if btn.Pressed() {
		bg = mygoui.RGBA(239, 68, 68, 0.20)
	} else if btn.Hovered() {
		bg = mygoui.RGBA(239, 68, 68, 0.16)
	}

	btn.Padding(4, 10).
		Radius(99).
		Background(bg).
		Border(1, border).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Shrink(0).
		Cursor(mygoui.CursorPointer).
		Label(text).
		Children(func() {
			mygoui.Text(c, text).FontSize(11).FontWeight(550).TextColor(fg).NoWrap()
		})
	return btn
}

// zenDangerSolidPillBtn renders a solid red CTA pill button for destructive confirmations.
func zenDangerSolidPillBtn(c *mygoui.Context, key, text string) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := zenDanger
	if btn.Pressed() {
		bg = mygoui.Hex("#B91C1C")
	} else if btn.Hovered() {
		bg = zenDangerDark
	}

	btn.Padding(6, 14).
		Radius(99).
		Background(bg).
		Border(1, bg).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Shrink(0).
		Cursor(mygoui.CursorPointer).
		Label(text).
		Children(func() {
			mygoui.Text(c, text).FontSize(12).FontWeight(600).TextColor(zenSelfBubbleInk).NoWrap()
		})
	return btn
}

// zenIconPillBtn renders a secondary/primary pill button with a leading Lucide SVG icon.
func zenIconPillBtn(c *mygoui.Context, key string, svg *mygoui.SVG, text string, primary, disabled bool) mygoui.Element {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := zenSurfaceSubtle
	border := zenBorder
	fg := zenInk

	if primary {
		bg = zenAccent
		border = zenAccent
		fg = zenSelfBubbleInk
		if !disabled {
			if btn.Pressed() {
				bg = zenAccentPressed
				border = zenAccentPressed
			} else if btn.Hovered() {
				bg = zenAccentHover
				border = zenAccentHover
			}
		}
	} else if !disabled {
		if btn.Pressed() {
			bg = zenProgressTrack
		} else if btn.Hovered() {
			bg = zenSurfaceHover
		}
	}

	btn.Padding(5, 11).
		Gap(5).
		Radius(99).
		Background(bg).
		Border(1, border).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Shrink(0).
		Disabled(disabled).
		Label(text)
	if !disabled {
		btn.Cursor(mygoui.CursorPointer)
	}
	btn.Children(func() {
		if svg != nil {
			mygoui.Icon(c, svg).Size(12, 12).TextColor(fg)
		}
		mygoui.Text(c, text).FontSize(11.5).FontWeight(550).TextColor(fg).NoWrap()
	})
	return btn
}

// zenFileTileColors returns the uppercase extension label and pastel tile colors for a file name.
func zenFileTileColors(fileName string) (ext string, bg, fg mygoui.Color) {
	raw := strings.ToUpper(strings.TrimPrefix(filepath.Ext(fileName), "."))
	ext = raw
	if ext == "" {
		ext = "FILE"
	} else if len(ext) > 4 {
		ext = ext[:4]
	}

	switch raw {
	case "PDF", "DOC", "DOCX", "MD", "TXT", "RTF":
		return ext, mygoui.Hex("#FFE4E6"), mygoui.Hex("#BE123C")
	case "ZIP", "TAR", "GZ", "TGZ", "7Z", "DMG", "RAR", "XZ":
		return ext, mygoui.Hex("#E0F2FE"), mygoui.Hex("#0369A1")
	case "MOV", "MP4", "MKV", "MP3", "AVI", "WEBM", "WAV", "M4A":
		return ext, mygoui.Hex("#E0E7FF"), mygoui.Hex("#4338CA")
	case "PNG", "JPG", "JPEG", "WEBP", "SVG", "GIF", "HEIC", "BMP":
		return ext, mygoui.Hex("#D1FAE5"), mygoui.Hex("#047857")
	default:
		return ext, mygoui.Hex("#F1F5F9"), mygoui.Hex("#334155")
	}
}
