package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
	"tailchat/internal/updater"
)

const tailchatGitHubURL = "https://github.com/jae-jae/tailchat"

// SettingsDraft holds editable form fields for the Settings modal.
type SettingsDraft struct {
	DeviceName          string
	LanguageIdx         int // 0: auto, 1: en, 2: zh
	ThemeIdx            int // 0: auto, 1: light, 2: dark
	DERPModeIdx         int // 0: default_tailcat, 1: custom_region, 2: custom_map_url
	RegionIDStr         string
	CustomHost          string
	CustomDERPPortStr   string
	CustomSTUNPortStr   string
	CustomRegionName    string
	DERPMapURL          string
	DirectListenEnabled bool
	DirectListenPortStr string
	PeerDirectAddrs     map[string]*string
	AutoDownloadMBStr   string
	RateLimitKBsStr     string
	MinimizeToTray      bool
	GlobalHotkey        string
	DownloadDir         string
}

// PeerDirectAddrPtr returns a stable heap pointer to the editable custom direct address
// string for deviceID so mygoui.TextInput bindings remain valid across frames.
func (d *SettingsDraft) PeerDirectAddrPtr(deviceID string) *string {
	if d.PeerDirectAddrs == nil {
		d.PeerDirectAddrs = make(map[string]*string)
	}
	if ptr, ok := d.PeerDirectAddrs[deviceID]; ok && ptr != nil {
		return ptr
	}
	s := ""
	d.PeerDirectAddrs[deviceID] = &s
	return &s
}

// SelectedLanguage returns the model.Language* constant corresponding to LanguageIdx.
func (d SettingsDraft) SelectedLanguage() string {
	switch d.LanguageIdx {
	case 1:
		return model.LanguageEnglish
	case 2:
		return model.LanguageChinese
	default:
		return model.LanguageAuto
	}
}

// SelectedTheme returns the model.Theme* constant corresponding to ThemeIdx.
func (d SettingsDraft) SelectedTheme() string {
	switch d.ThemeIdx {
	case 1:
		return model.ThemeLight
	case 2:
		return model.ThemeDark
	default:
		return model.ThemeAuto
	}
}

// SelectedLocale resolves the currently chosen draft language for live Settings preview.
func (d SettingsDraft) SelectedLocale() Locale {
	return ResolveLocale(d.SelectedLanguage())
}

// NewSettingsDraft initializes a SettingsDraft from an existing AppConfig.
func NewSettingsDraft(cfg model.AppConfig) SettingsDraft {
	langIdx := 0
	switch model.NormalizeLanguage(cfg.Language) {
	case model.LanguageEnglish:
		langIdx = 1
	case model.LanguageChinese:
		langIdx = 2
	default:
		langIdx = 0
	}

	themeIdx := 0
	switch model.NormalizeTheme(cfg.Theme) {
	case model.ThemeLight:
		themeIdx = 1
	case model.ThemeDark:
		themeIdx = 2
	default:
		themeIdx = 0
	}

	modeIdx := 0
	switch cfg.DERP.Mode {
	case model.DERPModeCustomRegion:
		modeIdx = 1
	case model.DERPModeCustomMapURL:
		modeIdx = 2
	default:
		modeIdx = 0
	}

	derpPort := cfg.DERP.CustomDERPPort
	if derpPort == 0 {
		derpPort = 443
	}
	stunPort := cfg.DERP.CustomSTUNPort
	if stunPort == 0 {
		stunPort = 3478
	}
	regionID := cfg.DERP.RegionID
	if regionID == 0 {
		regionID = 304
	}
	directPort := cfg.DirectListenPort
	if directPort <= 0 {
		directPort = model.DefaultDirectPort
	}

	peerDirect := make(map[string]*string, len(cfg.PeerDirectAddrs))
	for devID, addr := range cfg.PeerDirectAddrs {
		val := strings.TrimSpace(addr)
		peerDirect[devID] = &val
	}

	autoMB := cfg.AutoDownloadMaxBytes / (1024 * 1024)
	rateKBs := cfg.MaxDownloadBytesPerS / 1024

	return SettingsDraft{
		DeviceName:          cfg.DeviceName,
		LanguageIdx:         langIdx,
		ThemeIdx:            themeIdx,
		DERPModeIdx:         modeIdx,
		RegionIDStr:         strconv.Itoa(regionID),
		CustomHost:          cfg.DERP.CustomHost,
		CustomDERPPortStr:   strconv.Itoa(derpPort),
		CustomSTUNPortStr:   strconv.Itoa(stunPort),
		CustomRegionName:    cfg.DERP.CustomRegionName,
		DERPMapURL:          cfg.DERP.DERPMapURL,
		DirectListenEnabled: !cfg.DirectListenDisabled,
		DirectListenPortStr: strconv.Itoa(directPort),
		PeerDirectAddrs:     peerDirect,
		AutoDownloadMBStr:   strconv.FormatInt(autoMB, 10),
		RateLimitKBsStr:     strconv.FormatInt(rateKBs, 10),
		MinimizeToTray:      cfg.MinimizeToTray,
		GlobalHotkey:        cfg.GlobalHotkey,
		DownloadDir:         strings.TrimSpace(cfg.DownloadDir),
	}
}

// ToConfig validates and converts the draft back into a model.AppConfig.
func (d SettingsDraft) ToConfig() (model.AppConfig, error) {
	t := tr(d.SelectedLocale())

	regionID, err := parseIntDefault(d.RegionIDStr, 304)
	if err != nil || regionID < 0 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidRegionID)
	}
	derpPort, err := parseIntDefault(d.CustomDERPPortStr, 443)
	if err != nil || derpPort <= 0 || derpPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidDERPPort)
	}
	stunPort, err := parseIntDefault(d.CustomSTUNPortStr, 3478)
	if err != nil || stunPort <= 0 || stunPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidSTUNPort)
	}
	directPort, err := parseIntDefault(d.DirectListenPortStr, model.DefaultDirectPort)
	if err != nil || directPort <= 0 || directPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidDirectPort)
	}
	autoMB, err := parseInt64Default(d.AutoDownloadMBStr, 20)
	if err != nil || autoMB < 0 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidAutoDL)
	}
	rateKBs, err := parseInt64Default(d.RateLimitKBsStr, 0)
	if err != nil || rateKBs < 0 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidRateLim)
	}

	var peerDirect map[string]string
	if len(d.PeerDirectAddrs) > 0 {
		devIDs := make([]string, 0, len(d.PeerDirectAddrs))
		for devID := range d.PeerDirectAddrs {
			devIDs = append(devIDs, devID)
		}
		sort.Strings(devIDs)
		for _, devID := range devIDs {
			ptr := d.PeerDirectAddrs[devID]
			if ptr == nil {
				continue
			}
			raw := strings.TrimSpace(*ptr)
			if raw == "" {
				continue
			}
			norm, normErr := model.NormalizeDirectAddr(raw, model.DefaultDirectPort)
			if normErr != nil {
				return model.AppConfig{}, fmt.Errorf(t.ErrInvalidDirectAddrF, shortID(devID), normErr.Error())
			}
			if peerDirect == nil {
				peerDirect = make(map[string]string)
			}
			peerDirect[devID] = norm
		}
	}

	derpCfg := model.DERPConfig{
		RegionID:         regionID,
		CustomHost:       strings.TrimSpace(d.CustomHost),
		CustomDERPPort:   derpPort,
		CustomSTUNPort:   stunPort,
		CustomRegionName: strings.TrimSpace(d.CustomRegionName),
		DERPMapURL:       strings.TrimSpace(d.DERPMapURL),
	}

	switch d.DERPModeIdx {
	case 1:
		derpCfg.Mode = model.DERPModeCustomRegion
		if derpCfg.CustomHost == "" {
			return model.AppConfig{}, fmt.Errorf("%s", t.ErrNeedCustomHost)
		}
		if derpCfg.RegionID < 900 {
			derpCfg.RegionID = 900
		}
	case 2:
		derpCfg.Mode = model.DERPModeCustomMapURL
		if derpCfg.DERPMapURL == "" {
			return model.AppConfig{}, fmt.Errorf("%s", t.ErrNeedDERPMapURL)
		}
	default:
		derpCfg.Mode = model.DERPModeDefault
	}

	return model.AppConfig{
		DeviceName:           strings.TrimSpace(d.DeviceName),
		Language:             d.SelectedLanguage(),
		Theme:                d.SelectedTheme(),
		DERP:                 derpCfg,
		DirectListenPort:     directPort,
		DirectListenDisabled: !d.DirectListenEnabled,
		PeerDirectAddrs:      peerDirect,
		AutoDownloadMaxBytes: autoMB * 1024 * 1024,
		MaxDownloadBytesPerS: rateKBs * 1024,
		DownloadDir:          strings.TrimSpace(d.DownloadDir),
		MinimizeToTray:       d.MinimizeToTray,
		GlobalHotkey:         strings.TrimSpace(d.GlobalHotkey),
	}, nil
}

// renderDevicesPopover renders the lightweight Devices-only popover anchored to the header action bar.
func (a *App) renderDevicesPopover(c *mygoui.Context, anchor, actionsAnchor mygoui.Element, snap ViewSnapshot) {
	p := zenPal(c)
	t := tr(snap.Locale)
	mygoui.PopoverBase(c.Key("popover-devices"), anchor, &a.devicesPopoverOpen, func(panel mygoui.Element) {
		attachTarget := actionsAnchor
		if attachTarget == (mygoui.Element{}) {
			attachTarget = anchor
		}
		panel.AttachTo(attachTarget, mygoui.AnchorBottomRight, mygoui.AnchorTopRight).
			Margin(10, 0, 0, 0).
			Width(356).
			MaxWidthPercent(92).
			Padding(14).
			Radius(16).
			Background(p.Surface).
			Border(1, p.Border).
			Shadow(0, 14, 36, 0, p.ShadowPopover)

		mygoui.Column(c).Gap(10).FillWidth().Children(func() {
			// Popover header: title + online count badge.
			mygoui.Row(c).AlignItems(mygoui.Center).Gap(8).FillWidth().Children(func() {
				mygoui.Text(c, t.PairedDevices).FontSize(13.5).Bold().TextColor(p.Ink)
				mygoui.Spacer(c)

				badgeBg := p.InputFill
				badgeFg := p.InkMuted
				badgeText := t.OnlyThisDevice
				if snap.OnlineCount > 0 {
					badgeBg = p.DirectSoft
					badgeFg = p.DirectText
					badgeText = fmt.Sprintf(t.OnlineCountBadgeF, snap.OnlineCount)
				}
				mygoui.Row(c).
					Padding(2, 8).
					Radius(99).
					Background(badgeBg).
					Children(func() {
						mygoui.Text(c, badgeText).FontSize(11).FontWeight(600).TextColor(badgeFg).NoWrap()
					})
			})

			// Device list with bounded max height so expanding an inline IP accordion never overflows.
			if len(snap.Devices) == 0 {
				mygoui.Text(c, t.NoPairedDevices).FontSize(12).TextColor(p.InkMuted)
			} else {
				mygoui.Scroll(c.Key("popover-dev-scroll")).MaxHeight(420).FillWidth().Children(func() {
					mygoui.Column(c).Gap(7).FillWidth().Children(func() {
						for _, dev := range snap.Devices {
							a.renderPopoverDeviceCard(c, snap, dev)
						}
					})
				})
			}

			// Footer CTA: jump directly to dedicated QR pairing modal.
			footBtn := mygoui.ButtonBase(c.Key("btn-devices-to-pair"))
			footBg := p.AccentSoft
			footBorder := p.AccentBorder
			if footBtn.Pressed() {
				footBg = p.AccentSoftPress
				footBorder = p.Accent.Alpha(0.45)
			} else if footBtn.Hovered() {
				footBg = p.AccentSoftHover
				footBorder = p.Accent.Alpha(0.38)
			}
			footBtn.Height(34).
				FillWidth().
				Radius(99).
				Gap(6).
				Background(footBg).
				Border(1, footBorder).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Cursor(mygoui.CursorPointer).
				Label(t.PairNewDevice).
				Children(func() {
					mygoui.Icon(c, iconQRCode).Size(14, 14).TextColor(p.Accent)
					mygoui.Text(c, t.PairNewDevice).FontSize(12).FontWeight(600).TextColor(p.Accent).NoWrap()
				})
			if footBtn.Clicked() {
				a.devicesPopoverOpen = false
				a.openPairingFlow(0)
			}

			if errMsg := a.devicesErr; errMsg != "" {
				mygoui.Text(c, errMsg).FontSize(11.5).TextColor(p.Danger).Wrap()
			}
		})
	})
}

// renderPairingModal renders the dedicated 2-tab QR-first Pairing modal.
func (a *App) renderPairingModal(c *mygoui.Context, snap ViewSnapshot) {
	p := zenPal(c)
	t := tr(snap.Locale)
	mygoui.DialogBase(c.Key("modal-pair"), &a.pairOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(p.Backdrop)
		panel.Width(396).
			MaxWidthPercent(92).
			MaxHeightPercent(90).
			Padding(20).
			Radius(22).
			Background(p.Surface).
			Border(1, p.Border).
			Shadow(0, 24, 60, 0, p.ShadowModal)

		mygoui.Column(c).Gap(14).FillWidth().Children(func() {
			// Modal header: title + circular close button.
			mygoui.Row(c).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Text(c, t.PairDeviceTitle).FontSize(16).Bold().TextColor(p.Ink)
				mygoui.Spacer(c)
				if zenCircleBtn(c, "pair-close", iconX, t.Close, false).Clicked() {
					a.pairOpen = false
				}
			})

			// 2-tab segmented switcher.
			mygoui.Row(c).
				Padding(4).
				Gap(4).
				Radius(12).
				Background(p.InputFill).
				FillWidth().
				Children(func() {
					a.renderPairTabBtn(c, "pair-tab-qr", iconQRCode, t.TabScanQR, a.pairTab == 0, func() {
						a.pairTab = 0
						a.pairErr = ""
						a.ensurePairingCode()
					})
					a.renderPairTabBtn(c, "pair-tab-join", iconLink, t.TabJoinCode, a.pairTab == 1, func() {
						a.pairTab = 1
						a.pairErr = ""
					})
				})

			if a.pairTab == 0 {
				a.renderPairingQRTab(c, snap.Locale)
			} else {
				a.renderPairingJoinTab(c, snap.Locale)
			}

			if a.pairErr != "" {
				mygoui.Text(c, a.pairErr).FontSize(11.5).TextColor(p.Danger).Wrap()
			}
		})
	})
}

func (a *App) renderPairTabBtn(c *mygoui.Context, key string, icon *mygoui.SVG, label string, active bool, onClick func()) {
	p := zenPal(c)
	btn := mygoui.ButtonBase(c.Key(key))

	bg := mygoui.RGBA(0, 0, 0, 0)
	fg := p.InkSecondary
	if active {
		if p.Dark {
			bg = p.ControlActive
		} else {
			bg = p.Surface
		}
		fg = p.Ink
	} else if btn.Hovered() {
		fg = p.Ink
	}

	btn.Grow(1).
		Height(34).
		Padding(0, 10).
		Gap(6).
		Radius(9).
		Background(bg).
		Justify(mygoui.Center).
		AlignItems(mygoui.Center).
		Cursor(mygoui.CursorPointer).
		Label(label)
	if active {
		btn.Shadow(0, 1, 4, 0, p.ShadowSoft)
	}
	btn.Children(func() {
		mygoui.Icon(c, icon).Size(14, 14).TextColor(fg)
		mygoui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(fg).NoWrap()
	})
	if btn.Clicked() && onClick != nil {
		onClick()
	}
}

func (a *App) renderPairingQRTab(c *mygoui.Context, loc Locale) {
	p := zenPal(c)
	t := tr(loc)
	if !a.pairLoading && a.pairCode == "" && a.pairErr == "" {
		a.ensurePairingCode()
	}

	var rem time.Duration
	expired := !a.pairLoading && a.pairCode == ""
	if !a.pairLoading && !expired && a.pairToken != nil && a.pairToken.ExpiresAt > 0 {
		rem = time.Until(time.UnixMilli(a.pairToken.ExpiresAt))
		if rem <= 0 {
			expired = true
		}
	}

	var qrBmp *mygoui.Bitmap
	if !a.pairLoading && !expired {
		qrBmp = a.pairingQRBitmap()
	}
	qrFrameBg := p.Surface
	if qrBmp != nil {
		// Always preserve a crisp white quiet-zone around the QR matrix for reliable scanning.
		qrFrameBg = p.QRCardBg
	}

	// Subtle QR showcase card (never renders raw tailchat-pair:// plaintext).
	mygoui.Column(c).
		Padding(16).
		Gap(12).
		Radius(16).
		Background(p.SurfaceSubtle).
		Border(1, p.Border).
		AlignItems(mygoui.Center).
		FillWidth().
		Children(func() {
			// Crisp white QR frame when showing the QR code; themed surface for loading/expired states.
			mygoui.Column(c).
				Width(196).
				Height(196).
				Padding(10).
				Radius(16).
				Background(qrFrameBg).
				Border(1, p.Border).
				Shadow(0, 6, 18, 0, p.ShadowSoft).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Children(func() {
					if a.pairLoading {
						mygoui.Text(c, t.GeneratingQR).FontSize(12).TextColor(p.InkMuted)
					} else if expired {
						mygoui.Column(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, t.QRExpired).FontSize(12.5).Bold().TextColor(p.Ink).NoWrap()
							if zenPillBtn(c, "btn-regen-pair", t.Regenerate, true, false).Clicked() {
								a.refreshPairingCode()
							}
						})
					} else if qrBmp != nil {
						mygoui.Image(c, qrBmp).Size(176, 176)
					} else {
						mygoui.Text(c, t.GeneratingQR).FontSize(12).TextColor(p.InkMuted)
					}
				})

			// Meta status row: countdown pill + mesh hint.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Justify(mygoui.Center).Children(func() {
				if !a.pairLoading && !expired {
					if a.pairToken != nil && a.pairToken.ExpiresAt > 0 {
						c.After(time.Second)
						mins := int(rem.Minutes())
						secs := int(rem.Seconds()) % 60
						if secs < 0 {
							secs = 0
						}
						mygoui.Row(c).
							Padding(2, 8).
							Radius(99).
							Background(p.DirectSoft).
							Children(func() {
								mygoui.Text(c, fmt.Sprintf(t.ExpiresInF, mins, secs)).
									FontSize(11).
									FontWeight(650).
									TextColor(p.DirectText).
									NoWrap()
							})
					}
				} else if expired {
					mygoui.Row(c).
						Padding(2, 8).
						Radius(99).
						Background(p.DangerSoft).
						Children(func() {
							mygoui.Text(c, t.ExpiredBadge).FontSize(11).FontWeight(650).TextColor(p.Danger).NoWrap()
						})
				}
				mygoui.Text(c, t.MeshHint).FontSize(12).TextColor(p.InkSecondary).NoWrap()
			})
		})

	// Concise helper caption.
	mygoui.Text(c, t.QRCaption).
		FontSize(12).
		TextColor(p.InkSecondary).
		TextAlign(mygoui.Center).
		Wrap().
		FillWidth()

	// Bottom action row: one-click copy primary pill + circular refresh button.
	mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
		copied := !a.pairCopiedUntil.IsZero() && time.Now().Before(a.pairCopiedUntil)
		if copied {
			c.After(200 * time.Millisecond)
		}
		copyDisabled := a.pairLoading || expired || a.pairCode == ""

		copyBtn := mygoui.ButtonBase(c.Key("btn-copy-pair"))
		copyBg := p.AccentSolid
		copyFg := p.SelfBubbleInk
		copyIcon := iconCopy
		copyText := t.CopyPairCode

		if copyDisabled {
			copyBg = p.InkMuted
		} else if copied {
			copyBg = mygoui.Hex("#059669")
			copyIcon = iconCheck
			copyText = t.CopiedPairCode
		} else if copyBtn.Pressed() {
			copyBg = p.AccentPressed
		} else if copyBtn.Hovered() {
			copyBg = p.AccentHover
		}

		copyBtn.Grow(1).
			Height(38).
			Padding(0, 16).
			Gap(6).
			Radius(99).
			Background(copyBg).
			Justify(mygoui.Center).
			AlignItems(mygoui.Center).
			Disabled(copyDisabled).
			Label(copyText)
		if !copyDisabled {
			copyBtn.Cursor(mygoui.CursorPointer)
		}
		copyBtn.Children(func() {
			mygoui.Icon(c, copyIcon).Size(14, 14).TextColor(copyFg)
			mygoui.Text(c, copyText).FontSize(13).FontWeight(600).TextColor(copyFg).NoWrap()
		})
		if copyBtn.Clicked() && !copyDisabled {
			if err := a.writeClipboardText(a.pairCode, false); err != nil {
				a.pairErr = t.CopyPairFailPref + err.Error()
			} else {
				a.pairErr = ""
				a.pairCopiedUntil = time.Now().Add(1800 * time.Millisecond)
				a.state.SetToast(t.PairCodeCopied, false)
			}
		}

		refreshBtn := mygoui.ButtonBase(c.Key("btn-refresh-pair"))
		refBg := p.Surface
		refFg := p.InkSecondary
		if !a.pairLoading {
			if refreshBtn.Pressed() {
				refBg = p.ProgressTrack
				refFg = p.Ink
			} else if refreshBtn.Hovered() {
				refBg = p.SurfaceHover
				refFg = p.Ink
			}
		}
		refreshBtn.Size(38, 38).
			Radius(99).
			Background(refBg).
			Border(1, p.BorderStrong).
			Justify(mygoui.Center).
			AlignItems(mygoui.Center).
			Disabled(a.pairLoading).
			Label(t.RefreshQR).
			Tooltip(t.RefreshQR)
		if !a.pairLoading {
			refreshBtn.Cursor(mygoui.CursorPointer)
		}
		refreshBtn.Children(func() {
			mygoui.Icon(c, iconRefreshCw).Size(15, 15).TextColor(refFg)
		})
		if refreshBtn.Clicked() && !a.pairLoading {
			a.refreshPairingCode()
		}
	})
}

func (a *App) renderPairingJoinTab(c *mygoui.Context, loc Locale) {
	p := zenPal(c)
	t := tr(loc)
	inputBg := p.Surface
	if p.Dark {
		inputBg = p.InputFill
	}
	mygoui.Column(c).
		Padding(16).
		Gap(12).
		Radius(16).
		Background(p.SurfaceSubtle).
		Border(1, p.Border).
		FillWidth().
		Children(func() {
			mygoui.Text(c, t.JoinGroupTitle).FontSize(13).Bold().TextColor(p.Ink)
			mygoui.Text(c, t.JoinGroupDesc).
				FontSize(12).
				TextColor(p.InkSecondary).
				Wrap()

			var in mygoui.Element
			mygoui.Row(c).
				Radius(12).
				Background(inputBg).
				Border(1, p.BorderStrong).
				Padding(9, 12).
				AlignItems(mygoui.Center).
				FillWidth().
				Children(func() {
					in = mygoui.TextInputBase(c.Key("join-code-input"), &a.joinCodeInput).
						Placeholder(t.JoinPlaceholder).
						Label(t.PairingCodeLabel).
						FillWidth().
						FontSize(12.5).
						TextColor(p.Ink)
				})

			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
				pasteBtn := mygoui.ButtonBase(c.Key("btn-paste-pair"))
				pasteBg := p.Surface
				if pasteBtn.Pressed() {
					pasteBg = p.ProgressTrack
				} else if pasteBtn.Hovered() {
					pasteBg = p.SurfaceHover
				}
				pasteBtn.Height(38).
					Padding(0, 14).
					Gap(5).
					Radius(99).
					Background(pasteBg).
					Border(1, p.BorderStrong).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Cursor(mygoui.CursorPointer).
					Label(t.PasteFromClipboard).
					Children(func() {
						mygoui.Icon(c, iconClipboard).Size(14, 14).TextColor(p.Ink)
						mygoui.Text(c, t.PasteFromClipboard).FontSize(12.5).FontWeight(600).TextColor(p.Ink).NoWrap()
					})
				if pasteBtn.Clicked() {
					txt := ""
					if a.readClipboardText != nil {
						txt = strings.TrimSpace(a.readClipboardText())
					}
					if txt == "" {
						a.state.SetToast(t.ClipboardNoCode, false)
					} else {
						a.joinCodeInput = txt
						a.pairErr = ""
						a.state.SetToast(t.PastedPairCode, false)
					}
				}

				canJoin := strings.TrimSpace(a.joinCodeInput) != "" && !a.joining
				joinLabel := t.JoinNow
				if a.joining {
					joinLabel = t.Joining
				}
				joinBtn := mygoui.ButtonBase(c.Key("btn-join-group"))
				joinBg := p.AccentSolid
				if !canJoin {
					joinBg = p.InkMuted
				} else if joinBtn.Pressed() {
					joinBg = p.AccentPressed
				} else if joinBtn.Hovered() {
					joinBg = p.AccentHover
				}
				joinBtn.Grow(1).
					Height(38).
					Padding(0, 16).
					Radius(99).
					Background(joinBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Disabled(!canJoin).
					Label(joinLabel)
				if canJoin {
					joinBtn.Cursor(mygoui.CursorPointer)
				}
				joinBtn.Children(func() {
					mygoui.Text(c, joinLabel).FontSize(13).FontWeight(600).TextColor(p.SelfBubbleInk).NoWrap()
				})

				if (joinBtn.Clicked() || in.Submitted()) && canJoin {
					code := strings.TrimSpace(a.joinCodeInput)
					a.joining = true
					a.pairErr = ""
					a.dispatchAsync(func() {
						err := a.state.JoinWithPairingCode(context.Background(), code)
						a.asyncMu.Lock()
						a.pendingJoinDone = true
						if err != nil {
							a.pendingJoinErr = err.Error()
						} else {
							a.pendingJoinErr = ""
						}
						a.asyncMu.Unlock()
						if a.win != nil {
							a.win.Invalidate()
						}
					})
				}
			})
		})
}

func (a *App) renderPopoverDeviceCard(c *mygoui.Context, snap ViewSnapshot, dev model.Device) {
	p := zenPal(c)
	t := tr(snap.Locale)
	isSelf := dev.IsSelf || (snap.Identity.DeviceID != "" && dev.DeviceID == snap.Identity.DeviceID)
	peer, hasPeer := snap.Peers[dev.DeviceID]

	title := dev.Name
	if title == "" {
		title = shortID(dev.DeviceID)
	}

	if isSelf {
		a.renderPopoverSelfCard(c, snap, dev, title, t)
		return
	}

	customAddr := strings.TrimSpace(snap.Config.PeerDirectAddrs[dev.DeviceID])
	isExpanded := a.expandedDeviceID == dev.DeviceID

	pillText := t.Offline
	pillFg := p.InkMuted
	pillBg := p.InputFill
	subText := ""
	subColor := p.InkSecondary
	subMono := true

	if hasPeer && peer.Connected {
		switch peer.PathKind {
		case model.ConnPathCustomDirect:
			pillText = t.CustomDirect
			pillFg = p.CustomDirect
			pillBg = p.CustomDirectSoft
			subText = peer.Endpoint
		case model.ConnPathLAN:
			pillText = t.LANDirect
			pillFg = p.LANDirect
			pillBg = p.LANDirectSoft
			subText = peer.Endpoint
		case model.ConnPathDirect:
			pillText = t.P2PDirect
			pillFg = p.DirectText
			pillBg = p.DirectSoft
			subText = peer.Endpoint
		case model.ConnPathDERP:
			pillText = t.DERPRelay
			pillFg = p.DERPText
			pillBg = p.DERPSoft
			subText = peer.DERPRegion
			subMono = false
		default:
			pillText = string(peer.PathKind)
			pillFg = p.DirectText
			pillBg = p.DirectSoft
			subText = peer.Endpoint
		}
		if peer.LatencyMs > 0 {
			if subText != "" {
				subText += fmt.Sprintf(" · %.1f ms", peer.LatencyMs)
			} else {
				subText = fmt.Sprintf("%.1f ms", peer.LatencyMs)
			}
		}
		if subText == "" {
			if customAddr != "" {
				subText = fmt.Sprintf(t.CustomDirectTagF, customAddr)
			} else {
				subText = shortID(dev.DeviceID)
			}
		}
	} else if hasPeer && peer.PathKind == model.ConnPathConnecting {
		pillText = t.Connecting
		pillFg = p.InkSecondary
		pillBg = p.InputFill
		subColor = p.InkMuted
		if customAddr != "" {
			subText = fmt.Sprintf(t.CustomDirectTagF, customAddr)
		} else {
			subText = shortID(dev.DeviceID)
		}
	} else {
		subColor = p.InkMuted
		if customAddr != "" {
			subText = fmt.Sprintf(t.CustomDirectTagF, customAddr)
		} else {
			subText = shortID(dev.DeviceID)
		}
	}

	card := mygoui.Column(c.Key("dev-" + dev.DeviceID)).
		Radius(12).
		Clip().
		FillWidth()
	if isExpanded {
		card.Background(p.Surface).
			Border(1, p.AccentBorder).
			Shadow(0, 4, 14, 0, p.ShadowCard)
	} else {
		card.Background(p.SurfaceSubtle).
			Border(1, p.Border)
	}

	card.Children(func() {
		// Main device summary row.
		mygoui.Row(c).
			Padding(9, 10).
			Gap(8).
			AlignItems(mygoui.Center).
			FillWidth().
			Children(func() {
				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
					mygoui.Row(c).AlignItems(mygoui.Center).Gap(6).FillWidth().Children(func() {
						mygoui.Text(c, title).
							FontSize(12.5).
							Bold().
							TextColor(p.Ink).
							Shrink(1).
							MinWidth(0).
							SingleLine()
						mygoui.Row(c).
							Padding(1.5, 6).
							Radius(99).
							Background(pillBg).
							Shrink(0).
							Children(func() {
								mygoui.Text(c, pillText).
									FontSize(10).
									FontWeight(650).
									TextColor(pillFg).
									NoWrap()
							})
					})

					subEl := mygoui.Text(c, subText).
						FontSize(11).
						TextColor(subColor).
						FillWidth().
						SingleLine()
					if subMono {
						subEl.Font("SF Mono, Menlo, monospace")
					}
				})

				// Right action icon buttons: Direct IP accordion toggle + Remove device.
				mygoui.Row(c).AlignItems(mygoui.Center).Gap(5).Shrink(0).Children(func() {
					netBtn := mygoui.ButtonBase(c.Key("btn-dev-direct-" + dev.DeviceID))
					netBg := p.Surface
					netBorder := p.Border
					netFg := p.InkSecondary
					switch {
					case isExpanded:
						netBg = p.AccentSolid
						netBorder = p.AccentSolid
						netFg = p.SelfBubbleInk
						if netBtn.Pressed() {
							netBg = p.AccentPressed
							netBorder = p.AccentPressed
						} else if netBtn.Hovered() {
							netBg = p.AccentHover
							netBorder = p.AccentHover
						}
					case customAddr != "":
						netBg = p.CustomDirectSoft
						netBorder = p.CustomDirectBorder
						netFg = p.CustomDirect
						if netBtn.Pressed() {
							netBg = p.CustomDirect.Alpha(0.28)
						} else if netBtn.Hovered() {
							netBg = p.CustomDirect.Alpha(0.22)
						}
					default:
						if netBtn.Pressed() {
							netBg = p.ProgressTrack
							netBorder = p.BorderStrong
							netFg = p.Ink
						} else if netBtn.Hovered() {
							netBg = p.SurfaceHover
							netBorder = p.BorderStrong
							netFg = p.Ink
						}
					}
					netBtn.Size(27, 27).
						Radius(8).
						Background(netBg).
						Border(1, netBorder).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Cursor(mygoui.CursorPointer).
						Label(t.EditDirectIPTip).
						Tooltip(t.EditDirectIPTip).
						Children(func() {
							mygoui.Icon(c, iconNetwork).Size(13, 13).TextColor(netFg)
						})
					if netBtn.Clicked() {
						if isExpanded {
							a.expandedDeviceID = ""
							a.deviceDirectErr = ""
						} else {
							a.expandedDeviceID = dev.DeviceID
							a.deviceDirectInput = customAddr
							a.deviceDirectErr = ""
						}
					}

					rmBtn := mygoui.ButtonBase(c.Key("revoke-" + dev.DeviceID))
					rmBg := p.DangerSoft
					rmBorder := p.DangerBorder
					rmFg := p.Danger
					if rmBtn.Pressed() {
						rmBg = p.DangerSoftPress
						rmFg = p.DangerDark
					} else if rmBtn.Hovered() {
						rmBg = p.DangerSoftHover
						rmFg = p.DangerDark
					}
					rmBtn.Size(27, 27).
						Radius(8).
						Background(rmBg).
						Border(1, rmBorder).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Cursor(mygoui.CursorPointer).
						Label(t.Remove).
						Tooltip(t.Remove).
						Children(func() {
							mygoui.Icon(c, iconTrash).Size(13, 13).TextColor(rmFg)
						})
					if rmBtn.Clicked() {
						devID := dev.DeviceID
						if a.expandedDeviceID == devID {
							a.expandedDeviceID = ""
							a.deviceDirectInput = ""
							a.deviceDirectErr = ""
						}
						a.dispatchAsync(func() {
							err := a.state.RevokeDevice(context.Background(), devID)
							errStr := ""
							if err != nil {
								errStr = err.Error()
							}
							a.asyncMu.Lock()
							a.pendingDevicesErr = &errStr
							a.asyncMu.Unlock()
							a.invalidate()
						})
					}
				})
			})

		// Option A: inline accordion drawer for per-device custom direct IP.
		if isExpanded {
			mygoui.Box(c).Height(1).FillWidth().Background(p.Border)
			mygoui.Column(c).
				Padding(10).
				Gap(7).
				Background(p.AccordionBg).
				FillWidth().
				Children(func() {
					mygoui.Row(c).AlignItems(mygoui.Center).Gap(6).FillWidth().Children(func() {
						mygoui.Text(c, t.DeviceDirectTitle).FontSize(11).FontWeight(600).TextColor(p.InkSecondary).NoWrap()
						mygoui.Spacer(c)
						mygoui.Text(c, t.DeviceDirectHint).FontSize(10).TextColor(p.InkMuted).NoWrap()
					})

					var ipInput mygoui.Element
					var saveClicked, resetClicked bool
					mygoui.Row(c).AlignItems(mygoui.Center).Gap(6).FillWidth().Children(func() {
						ipInput = mygoui.TextInput(c.Key("dev-direct-input-"+dev.DeviceID), &a.deviceDirectInput).
							Placeholder(t.DeviceDirectPlace).
							Label(t.DeviceDirectTitle).
							Grow(1)

						saveBtn := mygoui.ButtonBase(c.Key("btn-dev-direct-save-" + dev.DeviceID))
						saveBg := p.AccentSolid
						if a.deviceDirectSaving {
							saveBg = p.InkMuted
						} else if saveBtn.Pressed() {
							saveBg = p.AccentPressed
						} else if saveBtn.Hovered() {
							saveBg = p.AccentHover
						}
						saveBtn.Height(30).
							Padding(0, 11).
							Radius(8).
							Background(saveBg).
							Justify(mygoui.Center).
							AlignItems(mygoui.Center).
							Shrink(0).
							Disabled(a.deviceDirectSaving).
							Label(t.DeviceDirectSave)
						if !a.deviceDirectSaving {
							saveBtn.Cursor(mygoui.CursorPointer)
						}
						saveBtn.Children(func() {
							mygoui.Text(c, t.DeviceDirectSave).FontSize(11.5).FontWeight(600).TextColor(p.SelfBubbleInk).NoWrap()
						})
						saveClicked = saveBtn.Clicked()

						if customAddr != "" {
							resetBtn := mygoui.ButtonBase(c.Key("btn-dev-direct-reset-" + dev.DeviceID))
							resetBg := p.Surface
							if !a.deviceDirectSaving {
								if resetBtn.Pressed() {
									resetBg = p.ProgressTrack
								} else if resetBtn.Hovered() {
									resetBg = p.SurfaceHover
								}
							}
							resetBtn.Height(30).
								Padding(0, 9).
								Radius(8).
								Background(resetBg).
								Border(1, p.BorderStrong).
								Justify(mygoui.Center).
								AlignItems(mygoui.Center).
								Shrink(0).
								Disabled(a.deviceDirectSaving).
								Label(t.DeviceDirectReset)
							if !a.deviceDirectSaving {
								resetBtn.Cursor(mygoui.CursorPointer)
							}
							resetBtn.Children(func() {
								mygoui.Text(c, t.DeviceDirectReset).FontSize(11.5).FontWeight(600).TextColor(p.InkSecondary).NoWrap()
							})
							resetClicked = resetBtn.Clicked()
						}
					})

					if (saveClicked || ipInput.Submitted()) && !a.deviceDirectSaving {
						a.savePeerDirectAddrAsync(dev.DeviceID, a.deviceDirectInput)
					} else if resetClicked && !a.deviceDirectSaving {
						a.deviceDirectInput = ""
						a.savePeerDirectAddrAsync(dev.DeviceID, "")
					}

					// Quick-fill chips for auto-discovered LAN addresses.
					if len(dev.DirectAddrs) > 0 {
						maxChips := len(dev.DirectAddrs)
						if maxChips > 2 {
							maxChips = 2
						}
						mygoui.Row(c).AlignItems(mygoui.Center).Gap(5).Wrap().FillWidth().Children(func() {
							mygoui.Text(c, t.DeviceAutoLANLabel).FontSize(10.5).TextColor(p.InkMuted).NoWrap()
							for i := 0; i < maxChips; i++ {
								addr := dev.DirectAddrs[i]
								chip := mygoui.ButtonBase(c.Key(fmt.Sprintf("btn-dev-lan-chip-%s-%d", dev.DeviceID, i)))
								chipBg := p.Surface
								chipBorder := p.BorderStrong
								chipFg := p.InkSecondary
								if chip.Pressed() {
									chipBg = p.AccentSoftPress
									chipBorder = p.Accent
									chipFg = p.Accent
								} else if chip.Hovered() {
									chipBg = p.AccentSoft
									chipBorder = p.AccentBorder
									chipFg = p.Accent
								}
								chip.Padding(2, 7).
									Radius(99).
									Background(chipBg).
									Border(1, chipBorder).
									AlignItems(mygoui.Center).
									Justify(mygoui.Center).
									Shrink(0).
									Cursor(mygoui.CursorPointer).
									Label(addr).
									Children(func() {
										mygoui.Text(c, addr).
											FontSize(10.5).
											Font("SF Mono, Menlo, monospace").
											TextColor(chipFg).
											NoWrap()
									})
								if chip.Clicked() {
									a.deviceDirectInput = addr
									a.deviceDirectErr = ""
								}
							}
						})
					}

					if a.deviceDirectErr != "" {
						mygoui.Text(c, a.deviceDirectErr).FontSize(11).TextColor(p.DangerDark).Wrap().FillWidth()
					}
				})
		}
	})
}

func (a *App) renderPopoverSelfCard(c *mygoui.Context, snap ViewSnapshot, dev model.Device, title string, t uiStrings) {
	p := zenPal(c)
	hasLAN := !snap.Config.DirectListenDisabled && len(snap.LocalDirectAddrs) > 0

	subText := shortID(dev.DeviceID)
	subColor := p.InkMuted
	subMono := true
	subTip := ""
	if hasLAN {
		subText = "LAN " + snap.LocalDirectAddrs[0]
		subColor = p.InkSecondary
		subTip = strings.Join(snap.LocalDirectAddrs, "\n")
	} else if snap.Config.DirectListenDisabled {
		subText = t.DirectListenerOff
		subMono = false
	}

	mygoui.Row(c.Key("dev-" + dev.DeviceID)).
		Padding(9, 10).
		Gap(8).
		Radius(12).
		Background(p.SurfaceSubtle).
		Border(1, p.Border).
		AlignItems(mygoui.Center).
		FillWidth().
		Children(func() {
			mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
				mygoui.Row(c).AlignItems(mygoui.Center).Gap(6).FillWidth().Children(func() {
					mygoui.Text(c, title).
						FontSize(12.5).
						Bold().
						TextColor(p.Ink).
						Shrink(1).
						MinWidth(0).
						SingleLine()
					mygoui.Row(c).
						Padding(1.5, 6).
						Radius(99).
						Background(p.AccentSoft).
						Shrink(0).
						Children(func() {
							mygoui.Text(c, t.ThisDevice).
								FontSize(10).
								FontWeight(650).
								TextColor(p.Accent).
								NoWrap()
						})
				})

				subEl := mygoui.Text(c, subText).
					FontSize(11).
					TextColor(subColor).
					FillWidth().
					SingleLine()
				if subMono {
					subEl.Font("SF Mono, Menlo, monospace")
				}
				if subTip != "" {
					subEl.Tooltip(subTip)
				}
			})

			if hasLAN {
				primary := snap.LocalDirectAddrs[0]
				copyBtn := mygoui.ButtonBase(c.Key("btn-popover-copy-lan"))
				copyBg := p.Surface
				copyFg := p.InkSecondary
				if copyBtn.Pressed() {
					copyBg = p.ProgressTrack
					copyFg = p.Ink
				} else if copyBtn.Hovered() {
					copyBg = p.SurfaceHover
					copyFg = p.Ink
				}
				copyBtn.Height(24).
					Padding(0, 8).
					Gap(4).
					Radius(99).
					Background(copyBg).
					Border(1, p.BorderStrong).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Cursor(mygoui.CursorPointer).
					Label(t.CopyLANShort).
					Tooltip(primary).
					Children(func() {
						mygoui.Icon(c, iconCopy).Size(11, 11).TextColor(copyFg)
						mygoui.Text(c, t.CopyLANShort).FontSize(11).FontWeight(600).TextColor(copyFg).NoWrap()
					})
				if copyBtn.Clicked() {
					if err := a.writeClipboardText(primary, false); err != nil {
						a.state.SetToast(t.CopyFailedPref+err.Error(), true)
					} else {
						a.state.SetToast(t.LANAddrCopiedToast, false)
					}
				}
			}
		})
}

func (a *App) renderSettingsModal(c *mygoui.Context, snap ViewSnapshot) {
	if !a.settingsOpen {
		return
	}

	p := zenPal(c)

	// Apply any directory path chosen asynchronously by the native folder picker.
	a.pendingDirMu.Lock()
	if a.pendingDownloadDir != nil {
		a.settingsDraft.DownloadDir = *a.pendingDownloadDir
		a.pendingDownloadDir = nil
	}
	a.pendingDirMu.Unlock()

	t := tr(a.settingsDraft.SelectedLocale())
	// While Clear Confirm dialog is open on top, keep Settings open underneath so
	// Escape / backdrop clicks dismiss only the top confirmation dialog.
	settingsOpenCopy := a.settingsOpen
	openPtr := &a.settingsOpen
	if a.clearConfirmOpen {
		openPtr = &settingsOpenCopy
	}

	mygoui.DialogBase(c.Key("modal-settings"), openPtr, func(backdrop, panel mygoui.Element) {
		backdrop.Background(p.Backdrop)
		panel.Width(480).
			MaxWidthPercent(94).
			Height(616).
			MaxHeightPercent(86).
			Padding(18, 18).
			Radius(18).
			Background(p.Surface).
			Border(1, p.Border).
			Shadow(0, 20, 48, 0, p.ShadowModal)

		mygoui.Column(c).Gap(14).Fill().Children(func() {
			// Modal header: Settings title + close button.
			mygoui.Row(c).AlignItems(mygoui.Center).FillWidth().Shrink(0).Children(func() {
				mygoui.Text(c, t.SettingsTitle).FontSize(16).Bold().TextColor(p.Ink)
				mygoui.Spacer(c)
				if zenCircleBtn(c, "settings-x", iconX, t.CloseSettings, false).Clicked() {
					a.settingsOpen = false
				}
			})

			// 4-tab segmented switcher: General · Transfer · Network · About.
			mygoui.Segmented(c.Key("settings-tab"), &a.settingsTab,
				t.TabGeneral,
				t.TabTransfer,
				t.TabNetwork,
				t.TabAbout,
			).Label(t.SettingsTitle).FillEqually().Shrink(0)

			mygoui.Scroll(c.Key(fmt.Sprintf("settings-tab-scroll-%d", a.settingsTab))).
				Grow(1).
				MinHeight(0).
				FillWidth().
				Children(func() {
					switch a.settingsTab {
					case 0:
						a.renderSettingsGeneralTab(c, t)
					case 1:
						a.renderSettingsTransferTab(c, t)
					case 2:
						a.renderSettingsNetworkTab(c, snap, t)
					default:
						a.renderSettingsAboutTab(c, snap, t)
					}
				})

			if a.settingsTab != 3 {
				a.renderSettingsSaveFooter(c, t)
			}
		})
	})
}

func settingsSectionCard(c *mygoui.Context, body func()) {
	p := zenPal(c)
	mygoui.Column(c).
		Padding(12, 12).
		Gap(10).
		Radius(13).
		Background(p.SurfaceSubtle).
		Border(1, p.Border).
		FillWidth().
		Children(body)
}

func (a *App) renderSettingsGeneralTab(c *mygoui.Context, t uiStrings) {
	p := zenPal(c)
	mygoui.Column(c).Gap(10).FillWidth().Children(func() {
		// Identity, Language & Appearance card.
		settingsSectionCard(c, func() {
			mygoui.Column(c).Gap(5).FillWidth().Children(func() {
				mygoui.Text(c, t.DeviceName).FontSize(12).FontWeight(600).TextColor(p.Ink)
				mygoui.TextInput(c.Key("set-device-name"), &a.settingsDraft.DeviceName).
					Placeholder(t.MyDevicePlace).
					Label(t.DeviceName).
					FillWidth()
			})

			mygoui.Column(c).Gap(5).FillWidth().Children(func() {
				mygoui.Text(c, t.Language).FontSize(12).FontWeight(600).TextColor(p.Ink)
				mygoui.Segmented(c.Key("set-language"), &a.settingsDraft.LanguageIdx,
					t.LanguageAuto,
					t.LanguageEnglish,
					t.LanguageChinese,
				).Label(t.Language).FillEqually()
			})

			mygoui.Column(c).Gap(5).FillWidth().Children(func() {
				mygoui.Text(c, t.Appearance).FontSize(12).FontWeight(600).TextColor(p.Ink)
				mygoui.Segmented(c.Key("set-theme"), &a.settingsDraft.ThemeIdx,
					t.ThemeAuto,
					t.ThemeLight,
					t.ThemeDark,
				).Label(t.Appearance).FillEqually()
			})
		})

		// Window behavior card (desktop-only: tray does not exist on iOS/Android).
		if isDesktopPlatform() {
			settingsSectionCard(c, func() {
				mygoui.Checkbox(c.Key("set-min-tray"), &a.settingsDraft.MinimizeToTray, t.MinimizeToTray)
			})
		}

		// Danger Zone: Clear All Chat History.
		mygoui.Row(c).
			Padding(11, 14).
			Gap(10).
			Radius(13).
			Background(p.DangerSoft).
			Border(1, p.DangerBorder).
			AlignItems(mygoui.Center).
			FillWidth().
			Children(func() {
				mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					mygoui.Text(c, t.ClearHistoryTitle).FontSize(12).Bold().TextColor(p.DangerDark)
					mygoui.Text(c, t.ClearHistoryNote).FontSize(11).TextColor(p.InkSecondary).Wrap()
				})

				if zenDangerPillBtn(c, "btn-clear-history", t.ClearAllBtn).Clicked() {
					a.clearConfirmOpen = true
				}
			})
	})
}

func (a *App) renderSettingsTransferTab(c *mygoui.Context, t uiStrings) {
	p := zenPal(c)
	mygoui.Column(c).Gap(10).FillWidth().Children(func() {
		// File Storage Path card (desktop-only: mobile platforms use sandboxed app storage).
		if isDesktopPlatform() {
			settingsSectionCard(c, func() {
				mygoui.Column(c).Gap(6).FillWidth().Children(func() {
					mygoui.Text(c, t.DownloadDirLabel).FontSize(12).FontWeight(600).TextColor(p.Ink)
					mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
						mygoui.TextInput(c.Key("set-download-dir"), &a.settingsDraft.DownloadDir).
							Placeholder(t.DownloadDirPlace).
							Label(t.DownloadDirLabel).
							Grow(1)
						if zenPillBtn(c, "btn-browse-dir", t.BrowseDir, false, false).Clicked() {
							a.triggerChooseDownloadDir()
						}
					})
					mygoui.Text(c, t.DownloadDirHint).FontSize(11).TextColor(p.InkMuted).Wrap()
				})
			})
		}

		// Auto-download threshold & Download speed limit card.
		settingsSectionCard(c, func() {
			mygoui.Row(c).Gap(10).FillWidth().Children(func() {
				mygoui.Column(c).Gap(5).Grow(1).Children(func() {
					mygoui.Text(c, t.AutoDownloadMB).FontSize(12).FontWeight(600).TextColor(p.Ink)
					mygoui.TextInput(c.Key("set-autodl-mb"), &a.settingsDraft.AutoDownloadMBStr).
						Placeholder("20").
						Label(t.AutoDownloadMB).
						FillWidth()
				})
				mygoui.Column(c).Gap(5).Grow(1).Children(func() {
					mygoui.Text(c, t.DownloadLimitKBs).FontSize(12).FontWeight(600).TextColor(p.Ink)
					mygoui.TextInput(c.Key("set-rate-kbs"), &a.settingsDraft.RateLimitKBsStr).
						Placeholder("0").
						Label(t.DownloadLimitKBs).
						FillWidth()
				})
			})
			mygoui.Text(c, t.TransferPolicyHint).FontSize(11).TextColor(p.InkMuted).Wrap()
		})
	})
}

func (a *App) renderSettingsNetworkTab(c *mygoui.Context, snap ViewSnapshot, t uiStrings) {
	p := zenPal(c)
	mygoui.Column(c).Gap(10).Padding(0, 0, 8, 0).FillWidth().Children(func() {
		// Card 1: LAN & Custom Direct TCP+mTLS card.
		settingsSectionCard(c, func() {
			mygoui.Column(c).Gap(3).FillWidth().Children(func() {
				mygoui.Text(c, t.DirectConnTitle).FontSize(12.5).Bold().TextColor(p.Ink)
				mygoui.Text(c, t.DirectConnHint).FontSize(11).TextColor(p.InkMuted).Wrap()
			})

			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Wrap().FillWidth().Children(func() {
				mygoui.Checkbox(c.Key("set-direct-enable"), &a.settingsDraft.DirectListenEnabled, t.DirectListenEnable)
				mygoui.Spacer(c)
				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Shrink(0).Children(func() {
					mygoui.Text(c, t.DirectListenPort).FontSize(11.5).TextColor(p.InkSecondary).NoWrap()
					mygoui.TextInput(c.Key("set-direct-port"), &a.settingsDraft.DirectListenPortStr).
						Placeholder("19840").
						Label(t.DirectListenPort).
						Width(78)
				})
			})

			if a.settingsDraft.DirectListenEnabled && len(snap.LocalDirectAddrs) > 0 {
				shown := snap.LocalDirectAddrs
				if len(shown) > 2 {
					shown = shown[:2]
				}
				summary := strings.Join(shown, ", ")
				primary := snap.LocalDirectAddrs[0]
				mygoui.Row(c).
					Padding(6, 9).
					Gap(8).
					Radius(9).
					Background(p.Surface).
					Border(1, p.Border).
					AlignItems(mygoui.Center).
					FillWidth().
					Children(func() {
						mygoui.Text(c, t.LocalLANAddrsLabel).FontSize(11).FontWeight(600).TextColor(p.InkSecondary).NoWrap()
						mygoui.Text(c, summary).
							FontSize(11).
							Font("SF Mono, Menlo, monospace").
							TextColor(p.Ink).
							Grow(1).
							MinWidth(0).
							SingleLine().
							Tooltip(strings.Join(snap.LocalDirectAddrs, "\n"))
						if zenPillBtn(c, "btn-copy-lan-addr", t.CopyLANAddr, false, false).Clicked() {
							if err := a.writeClipboardText(primary, false); err != nil {
								a.state.SetToast(t.CopyFailedPref+err.Error(), true)
							} else {
								a.state.SetToast(t.LANAddrCopiedToast, false)
							}
						}
					})
			}

			mygoui.Text(c, t.SettingsPeerIPMovedHint).FontSize(11).TextColor(p.InkMuted).Wrap()
		})

		// Card 2: DERP Relay fallback server card.
		settingsSectionCard(c, func() {
			mygoui.Column(c).Gap(3).FillWidth().Children(func() {
				mygoui.Text(c, t.DERPServer).FontSize(12.5).Bold().TextColor(p.Ink)
				mygoui.Text(c, t.DERPHint).FontSize(11).TextColor(p.InkMuted).Wrap()
			})

			mygoui.Segmented(c.Key("set-derp-mode"), &a.settingsDraft.DERPModeIdx,
				t.DERPDefault,
				t.DERPSelfHosted,
				t.DERPCustomMap,
			).Label(t.DERPModeLabel).FillEqually()

			switch a.settingsDraft.DERPModeIdx {
			case 0:
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
					mygoui.Text(c, t.PreferredRegionID).FontSize(12).TextColor(p.InkSecondary)
					mygoui.TextInput(c.Key("set-region-id"), &a.settingsDraft.RegionIDStr).
						Placeholder("304").
						Label("Region ID").
						Width(120)
				})
			case 1:
				mygoui.Column(c).Gap(8).FillWidth().Children(func() {
					mygoui.TextInput(c.Key("set-custom-host"), &a.settingsDraft.CustomHost).
						Placeholder("derp.example.com").
						Label(t.CustomDERPHost).
						FillWidth()
					mygoui.Row(c).Gap(6).FillWidth().Children(func() {
						mygoui.TextInput(c.Key("set-derp-port"), &a.settingsDraft.CustomDERPPortStr).
							Placeholder("443").
							Label(t.DERPPort).
							Grow(1)
						mygoui.TextInput(c.Key("set-stun-port"), &a.settingsDraft.CustomSTUNPortStr).
							Placeholder("3478").
							Label(t.STUNPort).
							Grow(1)
						mygoui.TextInput(c.Key("set-region-name"), &a.settingsDraft.CustomRegionName).
							Placeholder(t.CustomNodePlace).
							Label(t.RegionName).
							Grow(1)
					})
				})
			case 2:
				mygoui.TextInput(c.Key("set-derp-map-url"), &a.settingsDraft.DERPMapURL).
					Placeholder("https://example.com/derpmap.json").
					Label("DERPMap URL").
					FillWidth()
			}

			mygoui.Row(c).Gap(8).Justify(mygoui.Start).FillWidth().Children(func() {
				if zenPillBtn(c, "btn-hotswitch-derp", t.ApplyHotSwitchDERP, false, a.settingsSaving).Clicked() && !a.settingsSaving {
					cfg, err := a.settingsDraft.ToConfig()
					if err != nil {
						a.settingsErr = err.Error()
					} else {
						a.applySettingsAsync(cfg, true)
					}
				}
			})
		})
	})
}

func (a *App) renderSettingsSaveFooter(c *mygoui.Context, t uiStrings) {
	p := zenPal(c)
	if a.settingsErr != "" {
		mygoui.Text(c, a.settingsErr).FontSize(12).TextColor(p.Danger).Wrap().Shrink(0)
	}

	mygoui.Box(c).Height(1).FillWidth().Shrink(0).Background(p.Border)

	mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Shrink(0).Children(func() {
		if zenPillBtn(c, "settings-close", t.Close, false, a.settingsSaving).Clicked() && !a.settingsSaving {
			a.settingsOpen = false
		}
		if zenPillBtn(c, "btn-save-settings", t.SaveSettings, true, a.settingsSaving).Clicked() && !a.settingsSaving {
			cfg, err := a.settingsDraft.ToConfig()
			if err != nil {
				a.settingsErr = err.Error()
			} else {
				a.applySettingsAsync(cfg, false)
			}
		}
	})
}

func (a *App) renderSettingsAboutTab(c *mygoui.Context, snap ViewSnapshot, t uiStrings) {
	p := zenPal(c)
	up := snap.Update
	ver := strings.TrimSpace(up.CurrentVersion)
	if ver == "" {
		ver = updater.FallbackVersion
	}

	mygoui.Column(c).Gap(14).FillWidth().Children(func() {
		// Centered hero brand block: app icon, name, tagline, version pill.
		mygoui.Column(c).
			AlignItems(mygoui.Center).
			Gap(7).
			Padding(14, 8, 6, 8).
			FillWidth().
			Children(func() {
				if iconSrc := a.appIcon(); iconSrc != nil {
					mygoui.Row(c).
						Size(68, 68).
						Radius(16).
						Clip().
						Shadow(0, 8, 22, 0, p.ShadowCard).
						Children(func() {
							mygoui.Image(c, iconSrc).Size(68, 68).Fit(mygoui.Cover)
						})
				}

				mygoui.Text(c, "TailChat").FontSize(18).Bold().TextColor(p.Ink)
				mygoui.Text(c, t.AppTagline).FontSize(12).TextColor(p.InkSecondary)

				mygoui.Row(c).
					Padding(3, 10).
					Radius(99).
					Background(p.InputFill).
					Border(1, p.Border).
					Children(func() {
						mygoui.Text(c, "v"+ver).FontSize(11.5).FontWeight(600).TextColor(p.InkSecondary).NoWrap()
					})
			})

		// Software Update card (desktop-only: mobile updates are distributed via App Store / Play Store).
		if isDesktopPlatform() {
			mygoui.Row(c).
				Padding(12, 14).
				Gap(10).
				Radius(14).
				Background(p.SurfaceSubtle).
				Border(1, p.Border).
				AlignItems(mygoui.Center).
				FillWidth().
				Children(func() {
					mygoui.Row(c).
						Size(34, 34).
						Radius(10).
						Background(p.AccentSoft).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Children(func() {
							mygoui.Icon(c, iconSparkles).Size(16, 16).TextColor(p.Accent)
						})

					mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						mygoui.Text(c, t.SoftwareUpdates).FontSize(12.5).Bold().TextColor(p.Ink)
						statusLine := fmt.Sprintf(t.CurrentVersionF, ver)
						statusColor := p.InkSecondary
						switch {
						case up.Checking:
							statusLine = t.CheckingUpdates
						case up.Available && up.LatestVersion != "":
							statusLine = fmt.Sprintf(t.UpdateAvailableF, up.LatestVersion)
							statusColor = p.Accent
						case up.LastError != "":
							statusLine = t.UpdateCheckFailedPref + up.LastError
							statusColor = p.Danger
						case up.UpToDate:
							statusLine = t.UpToDate
							statusColor = p.DirectText
						}
						mygoui.Text(c, statusLine).
							FontSize(11.5).
							TextColor(statusColor).
							FillWidth().
							SingleLine()
					})

					if up.Available && up.LatestVersion != "" {
						if zenPillBtn(c, "btn-settings-view-update", t.ViewUpdate, true, false).Clicked() {
							a.settingsOpen = false
							a.updateModalOpen = true
						}
					} else {
						btnLabel := t.CheckForUpdates
						if up.Checking {
							btnLabel = t.CheckingUpdates
						}
						if zenPillBtn(c, "btn-check-updates", btnLabel, false, up.Checking).Clicked() {
							a.state.CheckForUpdates(context.Background(), true)
						}
					}
				})
		}

		// GitHub Repository card.
		ghBtn := mygoui.ButtonBase(c.Key("btn-open-github"))
		ghBg := p.SurfaceSubtle
		if ghBtn.Pressed() {
			ghBg = p.ProgressTrack
		} else if ghBtn.Hovered() {
			ghBg = p.SurfaceHover
		}
		ghBtn.Padding(12, 14).
			Gap(10).
			Radius(14).
			Background(ghBg).
			Border(1, p.Border).
			AlignItems(mygoui.Center).
			FillWidth().
			Cursor(mygoui.CursorPointer).
			Label("github.com/jae-jae/tailchat").
			Tooltip(tailchatGitHubURL).
			Children(func() {
				mygoui.Row(c).
					Size(34, 34).
					Radius(10).
					Background(p.Surface).
					Border(1, p.Border).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Children(func() {
						mygoui.Icon(c, iconGitHub).Size(17, 17).TextColor(p.Ink)
					})

				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
					mygoui.Text(c, t.GitHubRepoLabel).FontSize(12.5).Bold().TextColor(p.Ink)
					mygoui.Text(c, "github.com/jae-jae/tailchat").
						FontSize(11.5).
						TextColor(p.InkSecondary).
						FillWidth().
						SingleLine()
				})

				mygoui.Icon(c, iconExternalLink).Size(15, 15).TextColor(p.InkMuted)
			})
		if ghBtn.Clicked() {
			a.triggerOpenExternalURL(tailchatGitHubURL)
		}

		// Footer on Tab 4.
		mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
			if zenPillBtn(c, "settings-about-close", t.Close, false, false).Clicked() {
				a.settingsOpen = false
			}
		})
	})
}

func (a *App) triggerChooseDownloadDir() {
	if a.chooseDirDialog == nil {
		return
	}
	defaultDir := a.settingsDraft.DownloadDir
	go func() {
		chosen, err := a.chooseDirDialog(defaultDir)
		if err != nil {
			a.state.SetToast(tr(a.state.Locale()).ChooseDirFailPref+err.Error(), true)
			return
		}
		chosen = strings.TrimSpace(chosen)
		if chosen == "" {
			return
		}
		a.pendingDirMu.Lock()
		a.pendingDownloadDir = &chosen
		a.pendingDirMu.Unlock()
		if a.win != nil {
			a.win.Invalidate()
		} else {
			a.state.notifyInvalidate()
		}
	}()
}

func (a *App) renderClearConfirmModal(c *mygoui.Context, snap ViewSnapshot) {
	if !a.clearConfirmOpen {
		return
	}
	p := zenPal(c)
	t := tr(a.settingsDraft.SelectedLocale())
	if !a.settingsOpen {
		t = tr(snap.Locale)
	}

	mygoui.DialogBase(c.Key("modal-clear-confirm"), &a.clearConfirmOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(p.BackdropStrong)
		panel.Width(380).
			MaxWidthPercent(90).
			Padding(20).
			Radius(18).
			Background(p.Surface).
			Border(1, p.DangerBorder).
			Shadow(0, 24, 56, 0, p.ShadowModal)

		mygoui.Column(c).Gap(14).FillWidth().Children(func() {
			mygoui.Row(c).Gap(11).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Row(c).
					Size(36, 36).
					Radius(11).
					Background(p.DangerSoft).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Children(func() {
						mygoui.Icon(c, iconAlertTriangle).Size(18, 18).TextColor(p.Danger)
					})
				mygoui.Text(c, t.ClearConfirmTitle).FontSize(15).Bold().TextColor(p.Ink).Grow(1).Wrap()
			})

			mygoui.Text(c, t.ClearConfirmBody).
				FontSize(12.5).
				TextColor(p.InkSecondary).
				Wrap().
				FillWidth()

			mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
				if zenPillBtn(c, "clear-confirm-cancel", t.Cancel, false, false).Clicked() {
					a.clearConfirmOpen = false
				}
				if zenDangerSolidPillBtn(c, "clear-confirm-ok", t.ClearConfirmOK).Clicked() {
					a.clearConfirmOpen = false
					a.settingsOpen = false
					a.dispatchAsync(func() {
						_ = a.state.ClearAllMessages(context.Background())
					})
				}
			})
		})
	})
}

func parseIntDefault(s string, def int) (int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return def, nil
	}
	return strconv.Atoi(trimmed)
}

func parseInt64Default(s string, def int64) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return def, nil
	}
	return strconv.ParseInt(trimmed, 10, 64)
}
