package ui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"tailchat/internal/model"
)

// Locale represents a resolved UI display language.
type Locale string

const (
	LocaleEN Locale = "en"
	LocaleZH Locale = "zh"
)

var (
	systemLocaleOnce   sync.Once
	cachedSystemLocale Locale

	// systemLocaleDetector can be overridden in tests for deterministic Auto resolution.
	systemLocaleDetector = detectSystemLocaleCached
)

// ResolveLocale resolves a user language preference ("auto", "en", "zh") to a concrete UI Locale.
// Unknown values default to "auto"; unsupported system locales fall back to English (LocaleEN).
func ResolveLocale(pref string) Locale {
	switch model.NormalizeLanguage(pref) {
	case model.LanguageChinese:
		return LocaleZH
	case model.LanguageEnglish:
		return LocaleEN
	default:
		if systemLocaleDetector != nil {
			if loc := systemLocaleDetector(); loc == LocaleZH || loc == LocaleEN {
				return loc
			}
		}
		return LocaleEN
	}
}

func detectSystemLocaleCached() Locale {
	systemLocaleOnce.Do(func() {
		cachedSystemLocale = detectSystemLocale()
	})
	return cachedSystemLocale
}

func detectSystemLocale() Locale {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" || strings.EqualFold(raw, "C") || strings.EqualFold(raw, "POSIX") {
			continue
		}
		return parseSystemLocale(raw)
	}

	// macOS GUI apps launched from Finder/Dock often have empty LANG/LC_* env vars.
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			if first := extractFirstAppleLanguage(string(out)); first != "" {
				return parseSystemLocale(first)
			}
		}
		if out, err := exec.Command("defaults", "read", "-g", "AppleLocale").Output(); err == nil {
			if raw := strings.TrimSpace(string(out)); raw != "" {
				return parseSystemLocale(raw)
			}
		}
	}

	return LocaleEN
}

func extractFirstAppleLanguage(out string) string {
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || s == "(" || s == ")" {
			continue
		}
		s = strings.Trim(s, `", `)
		if s != "" {
			return s
		}
	}
	return ""
}

// parseSystemLocale maps any OS locale identifier to LocaleZH for Chinese locales
// and falls back to LocaleEN for all other or empty locales.
func parseSystemLocale(raw string) Locale {
	for _, part := range strings.Split(raw, ":") {
		s := strings.ToLower(strings.TrimSpace(part))
		if s == "" || s == "c" || s == "posix" {
			continue
		}
		if idx := strings.IndexAny(s, ".@"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.ReplaceAll(s, "_", "-")
		if s == "zh" || strings.HasPrefix(s, "zh-") {
			return LocaleZH
		}
		return LocaleEN
	}
	return LocaleEN
}

// uiStrings holds all localized user-facing UI strings.
type uiStrings struct {
	// Header
	SearchMessages     string
	PairedDevices      string
	SettingsAndDERP    string
	Pair               string
	PairNewDevice      string

	// Search drawer
	SearchPlaceholder  string
	ClearSearch        string

	// Drop overlay & toast
	DropOverlayHint    string
	DismissToast       string

	// Empty state
	EmptyTitle         string
	EmptySubtitle      string
	SearchEmptyTitleF  string
	SearchEmptySub     string

	// Timeline & bubbles
	TodayPrefix        string
	YesterdayPrefix    string
	ThisDevice         string
	Copy               string
	PrivateCopy        string
	Delete             string
	FilePrefix         string
	TransferPausedF    string
	TransferFailedF    string
	FileSeedersOneF    string
	FileSeedersManyF   string
	ClickToDownload    string
	Blake3Verified     string
	ResumableTransfer  string
	TransferRetryHint  string
	TransferProgress   string
	OpenFile             string
	OpenFileDirectly     string
	ShowInFolder         string
	Pause                string
	Cancel               string
	Resume               string
	DownloadNow          string
	RetryDownload        string
	OpenFileFailedPref   string
	ShowInFolderFailPref string

	// Composer
	DropStripIdle      string
	DropStripActive    string
	AttachFiles        string
	SelectFilesTitle   string
	PasteClipboard     string
	PrivateCopyOn      string
	PrivateCopyOff     string
	ComposerPlace      string
	ComposerLabel      string
	Send               string
	SendTooltip        string
	CopyFailedPref     string
	CopiedPrivateToast string
	CopiedToast        string
	OpenDialogFailPref string
	ClipboardEmpty     string

	// Devices popover
	OnlyThisDevice    string
	OnlineCountBadgeF string
	NoPairedDevices   string
	Offline           string
	P2PDirect         string
	DERPRelay         string
	Connecting        string
	Remove            string

	// Pairing modal
	PairDeviceTitle    string
	Close              string
	TabScanQR          string
	TabJoinCode        string
	QRExpired          string
	Regenerate         string
	GeneratingQR       string
	ExpiresInF         string
	ExpiredBadge       string
	MeshHint           string
	QRCaption          string
	CopyPairCode       string
	CopiedPairCode     string
	CopyPairFailPref   string
	PairCodeCopied     string
	RefreshQR          string
	RefreshedQRToast   string
	JoinGroupTitle     string
	JoinGroupDesc      string
	JoinPlaceholder    string
	PairingCodeLabel   string
	PasteFromClipboard string
	ClipboardNoCode    string
	PastedPairCode     string
	JoinNow            string
	Joining            string

	// Settings modal
	SettingsTitle      string
	TabGeneral         string
	TabTransfer        string
	TabNetwork         string
	TabAbout           string
	CloseSettings      string
	DeviceName         string
	MyDevicePlace      string
	Language           string
	LanguageAuto       string
	LanguageEnglish    string
	LanguageChinese    string
	DownloadDirLabel   string
	DownloadDirPlace   string
	DownloadDirHint    string
	BrowseDir          string
	ChooseDirTitle     string
	ChooseDirFailPref  string
	DERPServer         string
	DERPHint           string
	DERPModeLabel      string
	DERPDefault        string
	DERPSelfHosted     string
	DERPCustomMap      string
	PreferredRegionID  string
	CustomDERPHost     string
	DERPPort           string
	STUNPort           string
	RegionName         string
	CustomNodePlace    string
	ApplyHotSwitchDERP string
	DirectConnTitle       string
	DirectConnHint        string
	DirectListenEnable    string
	DirectListenPort      string
	LocalLANAddrsLabel    string
	CopyLANAddr           string
	LANAddrCopiedToast    string
	PeerDirectIPsLabel    string
	PeerDirectIPHint      string
	PeerDirectAutoPlaceF  string
	PeerDirectEmptyPlace  string
	NoPeersForDirect      string
	AutoDownloadMB     string
	DownloadLimitKBs   string
	TransferPolicyHint string
	MinimizeToTray     string
	ClearHistoryTitle  string
	ClearHistoryNote   string
	ClearAllBtn        string
	ClearConfirmTitle  string
	ClearConfirmBody   string
	ClearConfirmOK     string
	AppTagline         string
	GitHubRepoLabel    string
	SaveSettings       string

	// Settings validation errors
	ErrInvalidRegionID    string
	ErrInvalidDERPPort    string
	ErrInvalidSTUNPort    string
	ErrInvalidDirectPort  string
	ErrInvalidDirectAddrF string
	ErrInvalidAutoDL      string
	ErrInvalidRateLim     string
	ErrNeedCustomHost     string
	ErrNeedDERPMapURL     string

	// State toasts & errors
	ConnOffline         string
	ConnDirectDERPF     string
	ConnDirectF         string
	ConnDERPF           string
	ToastSendFailPref   string
	ToastFileFailPref    string
	ToastSendingFilePref string
	ToastFileSentPref    string
	ToastPasteFailPref  string
	ToastSentPref       string
	ToastDelFailPref    string
	ToastClearFailPref  string
	ToastHistoryCleared string
	ToastDLFailPref     string
	ToastPauseFailPref  string
	ToastCancelFailPre  string
	ToastPairFailPref   string
	ErrEmptyPairCode    string
	ToastJoinFailPref   string
	ToastJoinedGroup    string
	ToastRevokeFailPre  string
	ToastRevokedDevice  string
	ToastSaveFailPref   string
	ToastDERPFailPref   string
	ToastDERPSwitched   string
	ToastSettingsSaved  string

	// Desktop tray & notifications
	TrayShowHide      string
	TrayQuickPair     string
	TrayQuit          string
	TrayTooltip       string
	NotifySharedFileF string
	NotifyNewMessage  string

	// Auto-update
	SoftwareUpdates       string
	CurrentVersionF       string
	CheckForUpdates       string
	CheckingUpdates       string
	UpToDate              string
	UpdateAvailableF      string
	UpdateBannerF         string
	UpdateNow             string
	ViewUpdate            string
	DownloadRelease       string
	UpdateModalTitle      string
	UpdateVersionCompareF string
	ReleaseNotesLabel     string
	NoReleaseNotes        string
	InstallingUpdate      string
	InstallingUpdatePctF  string
	UpdateCheckFailedPref string
	UpdateInstallFailPref string
	DismissUpdate         string
	OpenURLFailPref       string

	// Media preview & lightbox
	PreviewImage       string
	LoadingPreview     string
	DownloadAndPreview string
	MediaBadgeGIF      string
	MediaBadgeVideo    string
	MediaBadgeAudio    string
	PlayAudio          string
	PauseAudio         string
	PlayVideo          string
	AudioPlayFailPref  string
	ClosePreview       string
	ClickToZoomHint    string
}

var stringsEN = uiStrings{
	SearchMessages:     "Search messages",
	PairedDevices:      "Paired Devices",
	SettingsAndDERP:    "Settings & DERP",
	Pair:               "Pair",
	PairNewDevice:      "Pair New Device",

	SearchPlaceholder:  "Search text or filenames…",
	ClearSearch:        "Clear search",

	DropOverlayHint:    "Drop files to broadcast to all paired devices",
	DismissToast:       "Dismiss",

	EmptyTitle:         "Sync text & files across your devices",
	EmptySubtitle:      "Type below, drop any file here, or click Pair in the top-right to connect devices.",
	SearchEmptyTitleF:  "No messages matching %q",
	SearchEmptySub:     "Try a different keyword or filename.",

	TodayPrefix:        "Today ",
	YesterdayPrefix:    "Yesterday ",
	ThisDevice:         "This device",
	Copy:               "Copy",
	PrivateCopy:        "Private copy",
	Delete:             "Delete",
	FilePrefix:         "File ",
	TransferPausedF:    "%s / %s · Paused",
	TransferFailedF:    "%s · Failed",
	FileSeedersOneF:    "%s · 1 seeder",
	FileSeedersManyF:   "%s · %d seeders",
	ClickToDownload:    "Click to download on demand",
	Blake3Verified:     "BLAKE3 verified",
	ResumableTransfer:  "Resumable transfer",
	TransferRetryHint:  "Interrupted · Retry anytime",
	TransferProgress:   "Transfer progress",
	OpenFile:             "Open",
	OpenFileDirectly:     "Click to open file",
	ShowInFolder:         "Show in Folder",
	Pause:                "Pause",
	Cancel:               "Cancel",
	Resume:               "Resume",
	DownloadNow:          "Download",
	RetryDownload:        "Retry",
	OpenFileFailedPref:   "Failed to open file: ",
	ShowInFolderFailPref: "Failed to reveal file: ",

	DropStripIdle:      "Drop files here or paste screenshots to broadcast to all devices",
	DropStripActive:    "Release to broadcast files to all devices",
	AttachFiles:        "Attach files",
	SelectFilesTitle:   "Select files to send",
	PasteClipboard:     "Paste clipboard",
	PrivateCopyOn:      "Private copy enabled (excluded from clipboard history)",
	PrivateCopyOff:     "Private copy disabled",
	ComposerPlace:      "Message, or drop files here…",
	ComposerLabel:      "Message input",
	Send:               "Send",
	SendTooltip:        "Send message (Enter)",
	CopyFailedPref:     "Copy failed: ",
	CopiedPrivateToast: "Copied privately (no history)",
	CopiedToast:        "Copied to clipboard",
	OpenDialogFailPref: "Failed to open file picker: ",
	ClipboardEmpty:     "Clipboard has no image, file, or text to send",

	OnlyThisDevice:    "This device",
	OnlineCountBadgeF: "%d online",
	NoPairedDevices:   "No paired devices yet",
	Offline:           "Offline",
	P2PDirect:         "P2P Direct",
	DERPRelay:         "DERP Relay",
	Connecting:        "Connecting…",
	Remove:            "Remove",

	PairDeviceTitle:    "Pair Device",
	Close:              "Close",
	TabScanQR:          "Scan QR",
	TabJoinCode:        "Enter Code",
	QRExpired:          "QR code expired",
	Regenerate:         "Regenerate",
	GeneratingQR:       "Generating QR code…",
	ExpiresInF:         "Expires in %d:%02d",
	ExpiredBadge:       "Expired",
	MeshHint:           "Pair once, sync across all devices",
	QRCaption:          "Scan with TailChat on your new device, or copy the pairing code if it has no camera.",
	CopyPairCode:       "Copy Pairing Code",
	CopiedPairCode:     "Pairing Code Copied",
	CopyPairFailPref:   "Failed to copy pairing code: ",
	PairCodeCopied:     "Pairing code copied to clipboard",
	RefreshQR:          "Refresh QR code",
	RefreshedQRToast:   "Pairing QR code refreshed",
	JoinGroupTitle:     "Join Existing Group",
	JoinGroupDesc:      "Copy the pairing code from any paired device and paste it below to establish encrypted P2P sync.",
	JoinPlaceholder:    "Paste tailchat-pair://…",
	PairingCodeLabel:   "Pairing code",
	PasteFromClipboard: "Paste",
	ClipboardNoCode:    "Clipboard is empty, copy a pairing code first",
	PastedPairCode:     "Pairing code pasted from clipboard",
	JoinNow:            "Join Now",
	Joining:            "Joining…",

	SettingsTitle:      "Settings",
	TabGeneral:         "General",
	TabTransfer:        "Transfer",
	TabNetwork:         "Network",
	TabAbout:           "About",
	CloseSettings:      "Close settings",
	DeviceName:         "Device Name",
	MyDevicePlace:      "My Device",
	Language:           "Language",
	LanguageAuto:       "Auto",
	LanguageEnglish:    "English",
	LanguageChinese:    "中文",
	DownloadDirLabel:   "File Storage Path",
	DownloadDirPlace:   "~/Downloads",
	DownloadDirHint:    "Received files are saved here; leave empty for default",
	BrowseDir:          "Browse",
	ChooseDirTitle:     "Choose File Storage Folder",
	ChooseDirFailPref:  "Failed to choose folder: ",
	DERPServer:         "DERP Relay Server",
	DERPHint:           "Encrypted traffic falls back to DERP relay when direct P2P is unavailable.",
	DERPModeLabel:      "DERP Mode",
	DERPDefault:        "Default tailcat",
	DERPSelfHosted:     "Self-hosted",
	DERPCustomMap:      "Custom Map",
	PreferredRegionID:  "Preferred Region ID",
	CustomDERPHost:     "Self-hosted DERP host",
	DERPPort:           "DERP port",
	STUNPort:           "STUN port",
	RegionName:         "Region name",
	CustomNodePlace:    "Custom node",
	ApplyHotSwitchDERP:    "Apply & Hot-Switch DERP",
	DirectConnTitle:       "LAN & Custom Direct",
	DirectConnHint:        "Tries encrypted direct TCP first (Custom IP > Auto LAN), falling back to DERP seamlessly.",
	DirectListenEnable:    "Listen for direct connections",
	DirectListenPort:      "Port",
	LocalLANAddrsLabel:    "This device LAN:",
	CopyLANAddr:           "Copy",
	LANAddrCopiedToast:    "Local direct address copied",
	PeerDirectIPsLabel:    "Custom Peer IP (optional)",
	PeerDirectIPHint:      "Leave blank for auto LAN discovery. Supports IP or IP:port (default 19840).",
	PeerDirectAutoPlaceF:  "Auto: %s",
	PeerDirectEmptyPlace:  "e.g. 192.168.1.10",
	NoPeersForDirect:      "Pair another device first to configure custom direct IPs.",
	AutoDownloadMB:        "Auto-download (MB)",
	DownloadLimitKBs:      "Speed limit (KB/s)",
	TransferPolicyHint:    "Files above the threshold wait for manual download; 0 = unlimited speed.",
	MinimizeToTray:        "Minimize to tray when closing window",
	ClearHistoryTitle:     "Clear All Chat History",
	ClearHistoryNote:      "Clears chat history across all paired devices; local files stay intact",
	ClearAllBtn:           "Clear All",
	ClearConfirmTitle:     "Clear all chat history?",
	ClearConfirmBody:      "This will sync and clear chat history on this device and all paired devices, while keeping locally saved files.",
	ClearConfirmOK:        "Clear All",
	AppTagline:            "Private P2P Device Mesh",
	GitHubRepoLabel:       "GitHub",
	SaveSettings:          "Save",

	ErrInvalidRegionID:    "Invalid Region ID",
	ErrInvalidDERPPort:    "Invalid DERP port",
	ErrInvalidSTUNPort:    "Invalid STUN port",
	ErrInvalidDirectPort:  "Invalid direct listen port (1–65535)",
	ErrInvalidDirectAddrF: "Invalid direct address (%s): %s",
	ErrInvalidAutoDL:      "Invalid auto-download threshold (MB)",
	ErrInvalidRateLim:     "Invalid download speed limit (KB/s)",
	ErrNeedCustomHost:     "Please enter a self-hosted derper hostname or IP",
	ErrNeedDERPMapURL:     "Please enter a custom DERPMap URL",

	ConnOffline:         "Offline · Waiting for peers",
	ConnDirectDERPF:     "%d online · P2P Direct + DERP",
	ConnDirectF:         "%d online · P2P Direct",
	ConnDERPF:           "%d online · DERP Relay",
	ToastSendFailPref:   "Send failed: ",
	ToastFileFailPref:    "File send failed: ",
	ToastSendingFilePref: "Sending file: ",
	ToastFileSentPref:    "Sent file: ",
	ToastPasteFailPref:  "Failed to send pasted content: ",
	ToastSentPref:       "Sent ",
	ToastDelFailPref:    "Delete failed: ",
	ToastClearFailPref:  "Failed to clear chat history: ",
	ToastHistoryCleared: "Chat history cleared",
	ToastDLFailPref:     "Download failed: ",
	ToastPauseFailPref:  "Pause failed: ",
	ToastCancelFailPre:  "Cancel failed: ",
	ToastPairFailPref:   "Failed to create pairing code: ",
	ErrEmptyPairCode:    "Pairing code cannot be empty",
	ToastJoinFailPref:   "Failed to join group: ",
	ToastJoinedGroup:    "Joined device group",
	ToastRevokeFailPre:  "Failed to remove device: ",
	ToastRevokedDevice:  "Device removed",
	ToastSaveFailPref:   "Failed to save settings: ",
	ToastDERPFailPref:   "Failed to switch DERP: ",
	ToastDERPSwitched:   "DERP server switched",
	ToastSettingsSaved:  "Settings saved",

	TrayShowHide:       "Show / Hide TailChat",
	TrayQuickPair:      "Quick Pair…",
	TrayQuit:           "Quit TailChat",
	TrayTooltip:        "TailChat — Private P2P Device Mesh",
	NotifySharedFileF:  "Shared file: %s (%s)",
	NotifyNewMessage:   "New message received",

	SoftwareUpdates:       "Software Updates",
	CurrentVersionF:       "Current version: v%s",
	CheckForUpdates:       "Check for Updates",
	CheckingUpdates:       "Checking…",
	UpToDate:              "You're up to date",
	UpdateAvailableF:      "Update available: v%s",
	UpdateBannerF:         "TailChat v%s is available",
	UpdateNow:             "Update & Restart",
	ViewUpdate:            "View Update",
	DownloadRelease:       "Download Release",
	UpdateModalTitle:      "Software Update",
	UpdateVersionCompareF: "v%s → v%s",
	ReleaseNotesLabel:     "Release Notes",
	NoReleaseNotes:        "Includes new features and stability improvements.",
	InstallingUpdate:      "Downloading & installing update…",
	InstallingUpdatePctF:  "Downloading update… %d%%",
	UpdateCheckFailedPref: "Update check failed: ",
	UpdateInstallFailPref: "Update failed: ",
	DismissUpdate:         "Dismiss",
	OpenURLFailPref:       "Failed to open link: ",

	PreviewImage:       "Preview image",
	LoadingPreview:     "Loading preview…",
	DownloadAndPreview: "Download & Preview",
	MediaBadgeGIF:      "GIF",
	MediaBadgeVideo:    "VIDEO",
	MediaBadgeAudio:    "AUDIO",
	PlayAudio:          "Play audio",
	PauseAudio:         "Pause audio",
	PlayVideo:          "Open video",
	AudioPlayFailPref:  "Cannot play audio: ",
	ClosePreview:       "Close preview",
	ClickToZoomHint:    "Click image to zoom",
}

var stringsZH = uiStrings{
	SearchMessages:     "搜索消息",
	PairedDevices:      "已配对设备",
	SettingsAndDERP:    "设置与 DERP",
	Pair:               "配对",
	PairNewDevice:      "配对新设备",

	SearchPlaceholder:  "搜索文字或文件名…",
	ClearSearch:        "清除搜索",

	DropOverlayHint:    "松开鼠标，将文件广播给所有已配对设备",
	DismissToast:       "关闭提示",

	EmptyTitle:         "随时跨设备同步文字与文件",
	EmptySubtitle:      "在下方输入文字、直接拖入任意文件到此窗口，或点击右上角「配对」按钮快速配对。",
	SearchEmptyTitleF:  "未找到包含 %q 的消息",
	SearchEmptySub:     "试试更换关键词或文件名重新搜索。",

	TodayPrefix:        "今天 ",
	YesterdayPrefix:    "昨天 ",
	ThisDevice:         "本机",
	Copy:               "复制",
	PrivateCopy:        "隐私复制",
	Delete:             "删除",
	FilePrefix:         "文件 ",
	TransferPausedF:    "%s / %s · 已暂停",
	TransferFailedF:    "%s · 传输失败",
	FileSeedersOneF:    "%s · 1 台设备有源",
	FileSeedersManyF:   "%s · %d 台设备有源",
	ClickToDownload:    "点击按需下载",
	Blake3Verified:     "BLAKE3 校验通过",
	ResumableTransfer:  "支持断点续传",
	TransferRetryHint:  "传输中断 · 可重新下载",
	TransferProgress:   "传输进度",
	OpenFile:             "打开文件",
	OpenFileDirectly:     "点击直接打开文件",
	ShowInFolder:         "打开所在目录",
	Pause:                "暂停",
	Cancel:               "取消",
	Resume:               "继续",
	DownloadNow:          "立即下载",
	RetryDownload:        "重试下载",
	OpenFileFailedPref:   "无法打开文件：",
	ShowInFolderFailPref: "无法打开所在目录：",

	DropStripIdle:      "拖入任意文件到此窗口，或点击粘贴截图广播给所有设备",
	DropStripActive:    "松开鼠标立即广播文件给所有设备",
	AttachFiles:        "选择文件",
	SelectFilesTitle:   "选择要发送的文件",
	PasteClipboard:     "粘贴剪贴板",
	PrivateCopyOn:      "已开启隐私复制（不写入剪贴板历史）",
	PrivateCopyOff:     "已关闭隐私复制",
	ComposerPlace:      "发消息，或直接拖入文件…",
	ComposerLabel:      "输入消息",
	Send:               "发送",
	SendTooltip:        "发送消息 (Enter)",
	CopyFailedPref:     "复制失败：",
	CopiedPrivateToast: "已隐私复制（不留历史）",
	CopiedToast:        "已复制到剪贴板",
	OpenDialogFailPref: "打开文件选择器失败：",
	ClipboardEmpty:     "剪贴板中没有可发送的图片、文件或文字",

	OnlyThisDevice:    "仅本机",
	OnlineCountBadgeF: "%d 台在线",
	NoPairedDevices:   "暂无已配对设备",
	Offline:           "离线",
	P2PDirect:         "P2P 直连",
	DERPRelay:         "DERP 中继",
	Connecting:        "正在连接…",
	Remove:            "移除",

	PairDeviceTitle:    "配对设备",
	Close:              "关闭",
	TabScanQR:          "扫码配对",
	TabJoinCode:        "输入配对码加入",
	QRExpired:          "配对二维码已过期",
	Regenerate:         "重新生成",
	GeneratingQR:       "正在生成二维码…",
	ExpiresInF:         "有效剩余 %d:%02d",
	ExpiredBadge:       "已过期",
	MeshHint:           "一次配对，全群自动互通",
	QRCaption:          "用新设备上的 TailChat 扫码即可加入；若对方无摄像头，可一键复制配对码发送。",
	CopyPairCode:       "一键复制配对码",
	CopiedPairCode:     "已复制配对码",
	CopyPairFailPref:   "复制配对码失败：",
	PairCodeCopied:     "配对码已复制到剪贴板",
	RefreshQR:          "刷新二维码",
	RefreshedQRToast:   "已刷新配对二维码",
	JoinGroupTitle:     "加入已有设备群",
	JoinGroupDesc:      "在另一台已配对设备上点击「一键复制配对码」，粘贴到下方即可与所有设备建立加密 P2P 同步。",
	JoinPlaceholder:    "粘贴 tailchat-pair://…",
	PairingCodeLabel:   "配对码",
	PasteFromClipboard: "从剪贴板粘贴",
	ClipboardNoCode:    "剪贴板为空，请先复制配对码",
	PastedPairCode:     "已从剪贴板填入配对码",
	JoinNow:            "立即加入",
	Joining:            "加入中…",

	SettingsTitle:      "设置",
	TabGeneral:         "通用",
	TabTransfer:        "传输",
	TabNetwork:         "网络",
	TabAbout:           "关于",
	CloseSettings:      "关闭设置",
	DeviceName:         "设备名称",
	MyDevicePlace:      "我的设备",
	Language:           "语言",
	LanguageAuto:       "自动",
	LanguageEnglish:    "English",
	LanguageChinese:    "中文",
	DownloadDirLabel:   "文件存储路径",
	DownloadDirPlace:   "~/Downloads",
	DownloadDirHint:    "接收的文件将保存到此目录，留空使用默认下载目录",
	BrowseDir:          "选择目录",
	ChooseDirTitle:     "选择文件存储目录",
	ChooseDirFailPref:  "选择目录失败：",
	DERPServer:         "DERP 中继服务器",
	DERPHint:           "P2P 直连受限时，加密流量将自动经由 DERP 中继转发。",
	DERPModeLabel:      "DERP 模式",
	DERPDefault:        "默认 tailcat",
	DERPSelfHosted:     "自建 derper",
	DERPCustomMap:      "自定义 DERPMap",
	PreferredRegionID:  "首选 Region ID",
	CustomDERPHost:     "自建 DERP 域名",
	DERPPort:           "DERP 端口",
	STUNPort:           "STUN 端口",
	RegionName:         "区域名称",
	CustomNodePlace:    "自建节点",
	ApplyHotSwitchDERP:    "应用并热切 DERP",
	DirectConnTitle:       "局域网 / 自定义直连",
	DirectConnHint:        "优先尝试加密直连（自定义 IP > 自动发现局域网 IP），不可达时自动无缝回退至 DERP 中继。",
	DirectListenEnable:    "启用局域网/自定义直连监听",
	DirectListenPort:      "端口",
	LocalLANAddrsLabel:    "本机局域网地址：",
	CopyLANAddr:           "复制",
	LANAddrCopiedToast:    "已复制本机直连地址",
	PeerDirectIPsLabel:    "设备自定义直连 IP（可选）",
	PeerDirectIPHint:      "留空则自动发现；支持填写 IP 或 IP:端口（默认 19840）。",
	PeerDirectAutoPlaceF:  "自动：%s",
	PeerDirectEmptyPlace:  "如 192.168.1.10",
	NoPeersForDirect:      "配对其他设备后，可在此为每台设备配置自定义直连 IP。",
	AutoDownloadMB:        "自动下载阈值 (MB)",
	DownloadLimitKBs:      "下载限速 (KB/s)",
	TransferPolicyHint:    "超过阈值的文件需手动点击下载；限速填 0 表示不限速。",
	MinimizeToTray:        "关闭窗口时最小化到托盘",
	ClearHistoryTitle:     "清空所有聊天记录",
	ClearHistoryNote:      "同步清空所有配对设备的聊天记录，本地文件不受影响",
	ClearAllBtn:           "一键清空",
	ClearConfirmTitle:     "确认清空所有聊天记录？",
	ClearConfirmBody:      "此操作将同步清空当前设备及所有已配对设备的聊天记录，但会保留本地已保存的文件。",
	ClearConfirmOK:        "确认清空",
	AppTagline:            "私有 P2P 设备互联",
	GitHubRepoLabel:       "GitHub",
	SaveSettings:          "保存设置",

	ErrInvalidRegionID:    "Region ID 无效",
	ErrInvalidDERPPort:    "DERP 端口无效",
	ErrInvalidSTUNPort:    "STUN 端口无效",
	ErrInvalidDirectPort:  "直连监听端口无效（1–65535）",
	ErrInvalidDirectAddrF: "直连地址无效（%s）：%s",
	ErrInvalidAutoDL:      "自动下载阈值 (MB) 无效",
	ErrInvalidRateLim:     "下载限速 (KB/s) 无效",
	ErrNeedCustomHost:     "请输入自建 derper 服务器域名或 IP",
	ErrNeedDERPMapURL:     "请输入自定义 DERPMap URL",

	ConnOffline:         "离线 · 等待设备连接",
	ConnDirectDERPF:     "%d 台设备在线 · P2P 直连 + DERP",
	ConnDirectF:         "%d 台设备在线 · P2P 直连",
	ConnDERPF:           "%d 台设备在线 · DERP 中继",
	ToastSendFailPref:   "发送失败：",
	ToastFileFailPref:    "发送文件失败：",
	ToastSendingFilePref: "正在发送文件：",
	ToastFileSentPref:    "已发送文件：",
	ToastPasteFailPref:  "发送粘贴内容失败：",
	ToastSentPref:       "已发送 ",
	ToastDelFailPref:    "删除失败：",
	ToastClearFailPref:  "清空聊天记录失败：",
	ToastHistoryCleared: "已清空所有聊天记录",
	ToastDLFailPref:     "下载失败：",
	ToastPauseFailPref:  "暂停失败：",
	ToastCancelFailPre:  "取消失败：",
	ToastPairFailPref:   "生成配对码失败：",
	ErrEmptyPairCode:    "配对码不能为空",
	ToastJoinFailPref:   "加入设备群失败：",
	ToastJoinedGroup:    "已加入设备群",
	ToastRevokeFailPre:  "移除设备失败：",
	ToastRevokedDevice:  "已移除设备",
	ToastSaveFailPref:   "保存设置失败：",
	ToastDERPFailPref:   "DERP 切换失败：",
	ToastDERPSwitched:   "已切换 DERP 服务器",
	ToastSettingsSaved:  "设置已保存",

	TrayShowHide:       "显示 / 隐藏 TailChat",
	TrayQuickPair:      "快速配对…",
	TrayQuit:           "退出 TailChat",
	TrayTooltip:        "TailChat — 私有 P2P 设备互联",
	NotifySharedFileF:  "发送了文件：%s (%s)",
	NotifyNewMessage:   "收到一条新消息",

	SoftwareUpdates:       "软件更新",
	CurrentVersionF:       "当前版本：v%s",
	CheckForUpdates:       "检查更新",
	CheckingUpdates:       "检查中…",
	UpToDate:              "已是最新版本",
	UpdateAvailableF:      "发现新版本：v%s",
	UpdateBannerF:         "发现 TailChat v%s 新版本",
	UpdateNow:             "立即更新并重启",
	ViewUpdate:            "查看更新",
	DownloadRelease:       "前往下载页",
	UpdateModalTitle:      "发现新版本",
	UpdateVersionCompareF: "v%s → v%s",
	ReleaseNotesLabel:     "更新内容",
	NoReleaseNotes:        "包含新特性与稳定性优化。",
	InstallingUpdate:      "正在下载并安装更新…",
	InstallingUpdatePctF:  "正在下载更新… %d%%",
	UpdateCheckFailedPref: "检查更新失败：",
	UpdateInstallFailPref: "安装更新失败：",
	DismissUpdate:         "暂不提醒",
	OpenURLFailPref:       "无法打开链接：",

	PreviewImage:       "查看大图",
	LoadingPreview:     "正在加载预览…",
	DownloadAndPreview: "下载并预览",
	MediaBadgeGIF:      "动图",
	MediaBadgeVideo:    "视频",
	MediaBadgeAudio:    "音频",
	PlayAudio:          "播放音频",
	PauseAudio:         "暂停播放",
	PlayVideo:          "播放视频",
	AudioPlayFailPref:  "无法播放音频：",
	ClosePreview:       "关闭预览",
	ClickToZoomHint:    "点击图片查看大图",
}

func tr(loc Locale) uiStrings {
	if loc == LocaleZH {
		return stringsZH
	}
	return stringsEN
}

func formatSeedersMeta(loc Locale, sizeStr string, seederCount int) string {
	t := tr(loc)
	if seederCount <= 0 {
		return sizeStr
	}
	if seederCount == 1 {
		return fmt.Sprintf(t.FileSeedersOneF, sizeStr)
	}
	return fmt.Sprintf(t.FileSeedersManyF, sizeStr, seederCount)
}

// FormatTimeChipLocale formats a UnixMilli timestamp into a localized timeline pill label.
func FormatTimeChipLocale(loc Locale, unixMilli int64) string {
	if unixMilli <= 0 {
		return ""
	}
	t := time.UnixMilli(unixMilli).Local()
	now := time.Now().Local()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	s := tr(loc)
	switch {
	case !t.Before(todayStart):
		return s.TodayPrefix + t.Format("15:04")
	case !t.Before(yesterdayStart):
		return s.YesterdayPrefix + t.Format("15:04")
	default:
		return t.Format("01-02 15:04")
	}
}
