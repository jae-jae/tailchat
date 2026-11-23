package ui

import (
	"context"
	"fmt"
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
	DeviceName        string
	LanguageIdx       int // 0: auto, 1: en, 2: zh
	DERPModeIdx       int // 0: default_tailcat, 1: custom_region, 2: custom_map_url
	RegionIDStr       string
	CustomHost        string
	CustomDERPPortStr string
	CustomSTUNPortStr string
	CustomRegionName  string
	DERPMapURL        string
	AutoDownloadMBStr string
	RateLimitKBsStr   string
	MinimizeToTray    bool
	GlobalHotkey      string
	DownloadDir       string
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

	autoMB := cfg.AutoDownloadMaxBytes / (1024 * 1024)
	rateKBs := cfg.MaxDownloadBytesPerS / 1024

	return SettingsDraft{
		DeviceName:        cfg.DeviceName,
		LanguageIdx:       langIdx,
		DERPModeIdx:       modeIdx,
		RegionIDStr:       strconv.Itoa(regionID),
		CustomHost:        cfg.DERP.CustomHost,
		CustomDERPPortStr: strconv.Itoa(derpPort),
		CustomSTUNPortStr: strconv.Itoa(stunPort),
		CustomRegionName:  cfg.DERP.CustomRegionName,
		DERPMapURL:        cfg.DERP.DERPMapURL,
		AutoDownloadMBStr: strconv.FormatInt(autoMB, 10),
		RateLimitKBsStr:   strconv.FormatInt(rateKBs, 10),
		MinimizeToTray:    cfg.MinimizeToTray,
		GlobalHotkey:      cfg.GlobalHotkey,
		DownloadDir:       strings.TrimSpace(cfg.DownloadDir),
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
	autoMB, err := parseInt64Default(d.AutoDownloadMBStr, 20)
	if err != nil || autoMB < 0 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidAutoDL)
	}
	rateKBs, err := parseInt64Default(d.RateLimitKBsStr, 0)
	if err != nil || rateKBs < 0 {
		return model.AppConfig{}, fmt.Errorf("%s", t.ErrInvalidRateLim)
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
		DERP:                 derpCfg,
		AutoDownloadMaxBytes: autoMB * 1024 * 1024,
		MaxDownloadBytesPerS: rateKBs * 1024,
		DownloadDir:          strings.TrimSpace(d.DownloadDir),
		MinimizeToTray:       d.MinimizeToTray,
		GlobalHotkey:         strings.TrimSpace(d.GlobalHotkey),
	}, nil
}

// renderDevicesPopover renders the lightweight Devices-only popover anchored to the header button.
func (a *App) renderDevicesPopover(c *mygoui.Context, anchor mygoui.Element, snap ViewSnapshot) {
	t := tr(snap.Locale)
	mygoui.PopoverBase(c.Key("popover-devices"), anchor, &a.devicesPopoverOpen, func(panel mygoui.Element) {
		panel.AttachTo(anchor, mygoui.AnchorBottomRight, mygoui.AnchorTopRight).
			Margin(8, 0, 0, 0).
			Width(300).
			Padding(14).
			Radius(16).
			Background(zenSurface).
			Border(1, zenBorder).
			Shadow(0, 14, 36, 0, mygoui.RGBA(15, 23, 42, 0.16))

		mygoui.Column(c).Gap(10).FillWidth().Children(func() {
			// Popover header: title + online count badge.
			mygoui.Row(c).AlignItems(mygoui.Center).Gap(8).FillWidth().Children(func() {
				mygoui.Text(c, t.PairedDevices).FontSize(13.5).Bold().TextColor(zenInk)
				mygoui.Spacer(c)

				badgeBg := zenInputFill
				badgeFg := zenInkMuted
				badgeText := t.OnlyThisDevice
				if snap.OnlineCount > 0 {
					badgeBg = zenDirectSoft
					badgeFg = zenDirectText
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

			// Compact device list (bounded scroll when there are many paired devices).
			if len(snap.Devices) == 0 {
				mygoui.Text(c, t.NoPairedDevices).FontSize(12).TextColor(zenInkMuted)
			} else if len(snap.Devices) > 4 {
				mygoui.Scroll(c.Key("popover-dev-scroll")).Height(220).FillWidth().Children(func() {
					mygoui.Column(c).Gap(7).FillWidth().Children(func() {
						for _, dev := range snap.Devices {
							a.renderPopoverDeviceCard(c, snap, dev)
						}
					})
				})
			} else {
				mygoui.Column(c).Gap(7).FillWidth().Children(func() {
					for _, dev := range snap.Devices {
						a.renderPopoverDeviceCard(c, snap, dev)
					}
				})
			}

			// Footer CTA: jump directly to dedicated QR pairing modal.
			footBtn := mygoui.ButtonBase(c.Key("btn-devices-to-pair"))
			footBg := zenAccentSoft
			footBorder := zenAccent.Alpha(0.25)
			if footBtn.Pressed() {
				footBg = mygoui.RGBA(10, 132, 255, 0.20)
				footBorder = zenAccent.Alpha(0.38)
			} else if footBtn.Hovered() {
				footBg = mygoui.RGBA(10, 132, 255, 0.16)
				footBorder = zenAccent.Alpha(0.34)
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
					mygoui.Icon(c, iconQRCode).Size(14, 14).TextColor(zenAccent)
					mygoui.Text(c, t.PairNewDevice).FontSize(12).FontWeight(600).TextColor(zenAccent).NoWrap()
				})
			if footBtn.Clicked() {
				a.devicesPopoverOpen = false
				a.openPairingFlow(0)
			}

			if errMsg := a.devicesErr; errMsg != "" {
				mygoui.Text(c, errMsg).FontSize(11.5).TextColor(zenDanger).Wrap()
			}
		})
	})
}

// renderPairingModal renders the dedicated 2-tab QR-first Pairing modal.
func (a *App) renderPairingModal(c *mygoui.Context, snap ViewSnapshot) {
	t := tr(snap.Locale)
	mygoui.DialogBase(c.Key("modal-pair"), &a.pairOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(mygoui.RGBA(15, 23, 42, 0.36))
		panel.Width(396).
			Padding(20).
			Radius(22).
			Background(zenSurface).
			Border(1, zenBorder).
			Shadow(0, 24, 60, 0, mygoui.RGBA(15, 23, 42, 0.22))

		mygoui.Column(c).Gap(14).FillWidth().Children(func() {
			// Modal header: title + circular close button.
			mygoui.Row(c).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Text(c, t.PairDeviceTitle).FontSize(16).Bold().TextColor(zenInk)
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
				Background(zenInputFill).
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
				mygoui.Text(c, a.pairErr).FontSize(11.5).TextColor(zenDanger).Wrap()
			}
		})
	})
}

func (a *App) renderPairTabBtn(c *mygoui.Context, key string, icon *mygoui.SVG, label string, active bool, onClick func()) {
	btn := mygoui.ButtonBase(c.Key(key))

	bg := mygoui.RGBA(0, 0, 0, 0)
	fg := zenInkSecondary
	if active {
		bg = zenSurface
		fg = zenInk
	} else if btn.Hovered() {
		fg = zenInk
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
		btn.Shadow(0, 1, 4, 0, mygoui.RGBA(15, 23, 42, 0.09))
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
	t := tr(loc)
	if a.pairCode == "" && a.pairErr == "" {
		a.ensurePairingCode()
	}

	var rem time.Duration
	expired := a.pairCode == ""
	if !expired && a.pairToken != nil && a.pairToken.ExpiresAt > 0 {
		rem = time.Until(time.UnixMilli(a.pairToken.ExpiresAt))
		if rem <= 0 {
			expired = true
		}
	}

	// Subtle QR showcase card (never renders raw tailchat-pair:// plaintext).
	mygoui.Column(c).
		Padding(16).
		Gap(12).
		Radius(16).
		Background(zenSurfaceSubtle).
		Border(1, zenBorder).
		AlignItems(mygoui.Center).
		FillWidth().
		Children(func() {
			// Crisp white QR frame.
			mygoui.Column(c).
				Width(196).
				Height(196).
				Padding(10).
				Radius(16).
				Background(zenSurface).
				Border(1, zenBorder).
				Shadow(0, 6, 18, 0, mygoui.RGBA(15, 23, 42, 0.07)).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Children(func() {
					if expired {
						mygoui.Column(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, t.QRExpired).FontSize(12.5).Bold().TextColor(zenInk).NoWrap()
							if zenPillBtn(c, "btn-regen-pair", t.Regenerate, true, false).Clicked() {
								a.refreshPairingCode()
							}
						})
					} else if qrBmp := a.pairingQRBitmap(); qrBmp != nil {
						mygoui.Image(c, qrBmp).Size(176, 176)
					} else {
						mygoui.Text(c, t.GeneratingQR).FontSize(12).TextColor(zenInkMuted)
					}
				})

			// Meta status row: countdown pill + mesh hint.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Justify(mygoui.Center).Children(func() {
				if !expired {
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
							Background(zenDirectSoft).
							Children(func() {
								mygoui.Text(c, fmt.Sprintf(t.ExpiresInF, mins, secs)).
									FontSize(11).
									FontWeight(650).
									TextColor(zenDirectText).
									NoWrap()
							})
					}
				} else {
					mygoui.Row(c).
						Padding(2, 8).
						Radius(99).
						Background(zenDangerSoft).
						Children(func() {
							mygoui.Text(c, t.ExpiredBadge).FontSize(11).FontWeight(650).TextColor(zenDanger).NoWrap()
						})
				}
				mygoui.Text(c, t.MeshHint).FontSize(12).TextColor(zenInkSecondary).NoWrap()
			})
		})

	// Concise helper caption.
	mygoui.Text(c, t.QRCaption).
		FontSize(12).
		TextColor(zenInkSecondary).
		TextAlign(mygoui.Center).
		Wrap().
		FillWidth()

	// Bottom action row: one-click copy primary pill + circular refresh button.
	mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
		copied := !a.pairCopiedUntil.IsZero() && time.Now().Before(a.pairCopiedUntil)
		if copied {
			c.After(200 * time.Millisecond)
		}
		copyDisabled := expired || a.pairCode == ""

		copyBtn := mygoui.ButtonBase(c.Key("btn-copy-pair"))
		copyBg := zenAccent
		copyFg := zenSelfBubbleInk
		copyIcon := iconCopy
		copyText := t.CopyPairCode

		if copyDisabled {
			copyBg = zenInkMuted
		} else if copied {
			copyBg = zenDirectText
			copyIcon = iconCheck
			copyText = t.CopiedPairCode
		} else if copyBtn.Pressed() {
			copyBg = zenAccentPressed
		} else if copyBtn.Hovered() {
			copyBg = zenAccentHover
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
		refBg := zenSurface
		refFg := zenInkSecondary
		if refreshBtn.Pressed() {
			refBg = zenProgressTrack
			refFg = zenInk
		} else if refreshBtn.Hovered() {
			refBg = zenInputFill
			refFg = zenInk
		}
		refreshBtn.Size(38, 38).
			Radius(99).
			Background(refBg).
			Border(1, zenBorderStrong).
			Justify(mygoui.Center).
			AlignItems(mygoui.Center).
			Cursor(mygoui.CursorPointer).
			Label(t.RefreshQR).
			Tooltip(t.RefreshQR).
			Children(func() {
				mygoui.Icon(c, iconRefreshCw).Size(15, 15).TextColor(refFg)
			})
		if refreshBtn.Clicked() {
			a.refreshPairingCode()
		}
	})
}

func (a *App) renderPairingJoinTab(c *mygoui.Context, loc Locale) {
	t := tr(loc)
	mygoui.Column(c).
		Padding(16).
		Gap(12).
		Radius(16).
		Background(zenSurfaceSubtle).
		Border(1, zenBorder).
		FillWidth().
		Children(func() {
			mygoui.Text(c, t.JoinGroupTitle).FontSize(13).Bold().TextColor(zenInk)
			mygoui.Text(c, t.JoinGroupDesc).
				FontSize(12).
				TextColor(zenInkSecondary).
				Wrap()

			var in mygoui.Element
			mygoui.Row(c).
				Radius(12).
				Background(zenSurface).
				Border(1, zenBorderStrong).
				Padding(9, 12).
				AlignItems(mygoui.Center).
				FillWidth().
				Children(func() {
					in = mygoui.TextInputBase(c.Key("join-code-input"), &a.joinCodeInput).
						Placeholder(t.JoinPlaceholder).
						Label(t.PairingCodeLabel).
						FillWidth().
						FontSize(12.5).
						TextColor(zenInk)
				})

			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
				pasteBtn := mygoui.ButtonBase(c.Key("btn-paste-pair"))
				pasteBg := zenSurface
				if pasteBtn.Pressed() {
					pasteBg = zenInputFill
				} else if pasteBtn.Hovered() {
					pasteBg = zenSurfaceSubtle
				}
				pasteBtn.Height(38).
					Padding(0, 14).
					Gap(5).
					Radius(99).
					Background(pasteBg).
					Border(1, zenBorderStrong).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Cursor(mygoui.CursorPointer).
					Label(t.PasteFromClipboard).
					Children(func() {
						mygoui.Icon(c, iconClipboard).Size(14, 14).TextColor(zenInk)
						mygoui.Text(c, t.PasteFromClipboard).FontSize(12.5).FontWeight(600).TextColor(zenInk).NoWrap()
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

				canJoin := strings.TrimSpace(a.joinCodeInput) != ""
				joinBtn := mygoui.ButtonBase(c.Key("btn-join-group"))
				joinBg := zenAccent
				if !canJoin {
					joinBg = zenInkMuted
				} else if joinBtn.Pressed() {
					joinBg = zenAccentPressed
				} else if joinBtn.Hovered() {
					joinBg = zenAccentHover
				}
				joinBtn.Grow(1).
					Height(38).
					Padding(0, 16).
					Radius(99).
					Background(joinBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Disabled(!canJoin).
					Label(t.JoinNow)
				if canJoin {
					joinBtn.Cursor(mygoui.CursorPointer)
				}
				joinBtn.Children(func() {
					mygoui.Text(c, t.JoinNow).FontSize(13).FontWeight(600).TextColor(zenSelfBubbleInk).NoWrap()
				})

				if (joinBtn.Clicked() || in.Submitted()) && canJoin {
					code := strings.TrimSpace(a.joinCodeInput)
					if err := a.state.JoinWithPairingCode(context.Background(), code); err != nil {
						a.pairErr = err.Error()
					} else {
						a.pairErr = ""
						a.joinCodeInput = ""
						a.pairOpen = false
					}
				}
			})
		})
}

func (a *App) renderPopoverDeviceCard(c *mygoui.Context, snap ViewSnapshot, dev model.Device) {
	t := tr(snap.Locale)
	isSelf := dev.IsSelf || (snap.Identity.DeviceID != "" && dev.DeviceID == snap.Identity.DeviceID)
	peer, hasPeer := snap.Peers[dev.DeviceID]

	title := dev.Name
	if title == "" {
		title = shortID(dev.DeviceID)
	}

	subtitle := t.Offline
	subColor := zenInkMuted
	online := false
	statusColor := zenInkMuted

	if isSelf {
		subtitle = t.ThisDevice
		subColor = zenAccent
		online = true
		statusColor = zenDirect
	} else if hasPeer && peer.Connected {
		online = true
		switch peer.PathKind {
		case model.ConnPathDirect:
			subtitle = t.P2PDirect
			subColor = zenDirectText
			statusColor = zenDirect
		case model.ConnPathDERP:
			subtitle = t.DERPRelay
			if peer.DERPRegion != "" {
				subtitle += " · " + peer.DERPRegion
			}
			subColor = zenDERPText
			statusColor = zenDERP
		default:
			subtitle = string(peer.PathKind)
			statusColor = zenDirect
		}
		if peer.LatencyMs > 0 {
			subtitle += fmt.Sprintf(" · %.1f ms", peer.LatencyMs)
		}
	} else if hasPeer && peer.PathKind == model.ConnPathConnecting {
		subtitle = t.Connecting
	}

	mygoui.Row(c.Key("dev-"+dev.DeviceID)).
		Padding(9, 11).
		Gap(8).
		Radius(12).
		Background(zenSurfaceSubtle).
		Border(1, zenBorder).
		AlignItems(mygoui.Center).
		FillWidth().
		Children(func() {
			mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
				mygoui.Text(c, title).FontSize(12.5).Bold().TextColor(zenInk).FillWidth().SingleLine()
				mygoui.Text(c, subtitle).FontSize(11).TextColor(subColor).FillWidth().SingleLine()
			})

			if online {
				mygoui.Icon(c, iconCheckCircle).Size(15, 15).TextColor(statusColor)
			} else {
				mygoui.Icon(c, iconCircleDashed).Size(15, 15).TextColor(zenInkMuted)
			}

			if !isSelf {
				if zenDangerPillBtn(c, "revoke-"+dev.DeviceID, t.Remove).Clicked() {
					if err := a.state.RevokeDevice(context.Background(), dev.DeviceID); err != nil {
						a.devicesErr = err.Error()
					} else {
						a.devicesErr = ""
					}
				}
			}
		})
}

func (a *App) renderSettingsModal(c *mygoui.Context, snap ViewSnapshot) {
	if !a.settingsOpen {
		return
	}

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
		backdrop.Background(mygoui.RGBA(15, 23, 42, 0.32))
		panel.Width(440).
			Padding(18, 20).
			Radius(18).
			Background(zenSurface).
			Border(1, zenBorder).
			Shadow(0, 20, 48, 0, mygoui.RGBA(15, 23, 42, 0.22))

		mygoui.Column(c).Gap(12).FillWidth().Children(func() {
			// Modal header: Settings title + close button.
			mygoui.Row(c).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Text(c, t.SettingsTitle).FontSize(16).Bold().TextColor(zenInk)
				mygoui.Spacer(c)
				if zenCircleBtn(c, "settings-x", iconX, t.CloseSettings, false).Clicked() {
					a.settingsOpen = false
				}
			})

			// 2-tab segmented switcher: General · About.
			mygoui.Segmented(c.Key("settings-tab"), &a.settingsTab,
				t.TabGeneral,
				t.TabAbout,
			).Label(t.SettingsTitle).FillEqually()

			if a.settingsTab == 0 {
				a.renderSettingsGeneralTab(c, t)
			} else {
				a.renderSettingsAboutTab(c, snap, t)
			}
		})
	})
}

func (a *App) renderSettingsGeneralTab(c *mygoui.Context, t uiStrings) {
	mygoui.Scroll(c.Key("settings-general-scroll")).Height(460).FillWidth().Children(func() {
		mygoui.Column(c).Gap(9).Padding(2, 6, 4, 2).FillWidth().Children(func() {
			// 1. Device Name.
			mygoui.Column(c).Gap(4).FillWidth().Children(func() {
				mygoui.Text(c, t.DeviceName).FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.TextInput(c.Key("set-device-name"), &a.settingsDraft.DeviceName).
					Placeholder(t.MyDevicePlace).
					Label(t.DeviceName).
					FillWidth()
			})

			// 2. Language preference: Auto (fallback English), English, Chinese.
			mygoui.Column(c).Gap(4).FillWidth().Children(func() {
				mygoui.Text(c, t.Language).FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.Segmented(c.Key("set-language"), &a.settingsDraft.LanguageIdx,
					t.LanguageAuto,
					t.LanguageEnglish,
					t.LanguageChinese,
				).Label(t.Language).FillEqually()
			})

			// 3. File Storage Path.
			mygoui.Column(c).Gap(4).FillWidth().Children(func() {
				mygoui.Text(c, t.DownloadDirLabel).FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
					mygoui.TextInput(c.Key("set-download-dir"), &a.settingsDraft.DownloadDir).
						Placeholder(t.DownloadDirPlace).
						Label(t.DownloadDirLabel).
						Grow(1)
					if zenPillBtn(c, "btn-browse-dir", t.BrowseDir, false, false).Clicked() {
						a.triggerChooseDownloadDir()
					}
				})
			})

			// 4. DERP Relay Server.
			mygoui.Column(c).Gap(6).FillWidth().Children(func() {
				mygoui.Text(c, t.DERPServer).FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.Segmented(c.Key("set-derp-mode"), &a.settingsDraft.DERPModeIdx,
					t.DERPDefault,
					t.DERPSelfHosted,
					t.DERPCustomMap,
				).Label(t.DERPModeLabel).FillEqually()

				switch a.settingsDraft.DERPModeIdx {
				case 0:
					mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
						mygoui.Text(c, t.PreferredRegionID).FontSize(12).TextColor(zenInkSecondary)
						mygoui.TextInput(c.Key("set-region-id"), &a.settingsDraft.RegionIDStr).
							Placeholder("304").
							Label("Region ID").
							Width(120)
					})
				case 1:
					mygoui.Column(c).Gap(6).FillWidth().Children(func() {
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

				mygoui.Row(c).Gap(8).Children(func() {
					if zenPillBtn(c, "btn-hotswitch-derp", t.ApplyHotSwitchDERP, true, false).Clicked() {
						cfg, err := a.settingsDraft.ToConfig()
						if err != nil {
							a.settingsErr = err.Error()
						} else if err := a.state.ApplySettings(context.Background(), cfg, true); err != nil {
							a.settingsErr = err.Error()
						} else {
							a.settingsErr = ""
							a.settingsOpen = false
						}
					}
				})
			})

			mygoui.Box(c).Height(1).FillWidth().Background(zenBorder)

			// 5. Auto-download threshold & Download speed limit.
			mygoui.Row(c).Gap(10).FillWidth().Children(func() {
				mygoui.Column(c).Gap(4).Grow(1).Children(func() {
					mygoui.Text(c, t.AutoDownloadMB).FontSize(12).FontWeight(600).TextColor(zenInk)
					mygoui.TextInput(c.Key("set-autodl-mb"), &a.settingsDraft.AutoDownloadMBStr).
						Placeholder("20").
						Label(t.AutoDownloadMB).
						FillWidth()
				})
				mygoui.Column(c).Gap(4).Grow(1).Children(func() {
					mygoui.Text(c, t.DownloadLimitKBs).FontSize(12).FontWeight(600).TextColor(zenInk)
					mygoui.TextInput(c.Key("set-rate-kbs"), &a.settingsDraft.RateLimitKBsStr).
						Placeholder("0").
						Label(t.DownloadLimitKBs).
						FillWidth()
				})
			})

			// 6. Minimize to tray.
			mygoui.Checkbox(c.Key("set-min-tray"), &a.settingsDraft.MinimizeToTray, t.MinimizeToTray)

			mygoui.Box(c).Height(1).FillWidth().Background(zenBorder)

			// 7. Danger Zone: Clear All Chat History.
			mygoui.Row(c).
				Padding(9, 12).
				Gap(10).
				Radius(12).
				Background(zenDangerSoft).
				Border(1, mygoui.RGBA(239, 68, 68, 0.22)).
				AlignItems(mygoui.Center).
				FillWidth().
				Children(func() {
					mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						mygoui.Text(c, t.ClearHistoryTitle).FontSize(12).Bold().TextColor(zenDangerDark)
						mygoui.Text(c, t.ClearHistoryNote).FontSize(11).TextColor(zenInkSecondary).Wrap()
					})

					if zenDangerPillBtn(c, "btn-clear-history", t.ClearAllBtn).Clicked() {
						a.clearConfirmOpen = true
					}
				})
		})
	})

	if a.settingsErr != "" {
		mygoui.Text(c, a.settingsErr).FontSize(12).TextColor(zenDanger).Wrap()
	}

	// Footer actions on Tab 0.
	mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
		if zenPillBtn(c, "settings-close", t.Close, false, false).Clicked() {
			a.settingsOpen = false
		}
		if zenPillBtn(c, "btn-save-settings", t.SaveSettings, true, false).Clicked() {
			cfg, err := a.settingsDraft.ToConfig()
			if err != nil {
				a.settingsErr = err.Error()
			} else if err := a.state.ApplySettings(context.Background(), cfg, false); err != nil {
				a.settingsErr = err.Error()
			} else {
				a.settingsErr = ""
				a.settingsOpen = false
			}
		}
	})
}

func (a *App) renderSettingsAboutTab(c *mygoui.Context, snap ViewSnapshot, t uiStrings) {
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
						Shadow(0, 8, 22, 0, mygoui.RGBA(15, 23, 42, 0.14)).
						Children(func() {
							mygoui.Image(c, iconSrc).Size(68, 68).Fit(mygoui.Cover)
						})
				}

				mygoui.Text(c, "TailChat").FontSize(18).Bold().TextColor(zenInk)
				mygoui.Text(c, t.AppTagline).FontSize(12).TextColor(zenInkSecondary)

				mygoui.Row(c).
					Padding(3, 10).
					Radius(99).
					Background(zenInputFill).
					Border(1, zenBorder).
					Children(func() {
						mygoui.Text(c, "v"+ver).FontSize(11.5).FontWeight(600).TextColor(zenInkSecondary).NoWrap()
					})
			})

		// Software Update card.
		mygoui.Row(c).
			Padding(12, 14).
			Gap(10).
			Radius(14).
			Background(zenSurfaceSubtle).
			Border(1, zenBorder).
			AlignItems(mygoui.Center).
			FillWidth().
			Children(func() {
				mygoui.Row(c).
					Size(34, 34).
					Radius(10).
					Background(zenAccentSoft).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Children(func() {
						mygoui.Icon(c, iconSparkles).Size(16, 16).TextColor(zenAccent)
					})

				mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					mygoui.Text(c, t.SoftwareUpdates).FontSize(12.5).Bold().TextColor(zenInk)
					statusLine := fmt.Sprintf(t.CurrentVersionF, ver)
					statusColor := zenInkSecondary
					switch {
					case up.Checking:
						statusLine = t.CheckingUpdates
					case up.Available && up.LatestVersion != "":
						statusLine = fmt.Sprintf(t.UpdateAvailableF, up.LatestVersion)
						statusColor = zenAccent
					case up.LastError != "":
						statusLine = t.UpdateCheckFailedPref + up.LastError
						statusColor = zenDanger
					case up.UpToDate:
						statusLine = t.UpToDate
						statusColor = zenDirectText
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

		// GitHub Repository card.
		ghBtn := mygoui.ButtonBase(c.Key("btn-open-github"))
		ghBg := zenSurfaceSubtle
		if ghBtn.Pressed() {
			ghBg = zenInputFill
		} else if ghBtn.Hovered() {
			ghBg = zenSurfaceHover
		}
		ghBtn.Padding(12, 14).
			Gap(10).
			Radius(14).
			Background(ghBg).
			Border(1, zenBorder).
			AlignItems(mygoui.Center).
			FillWidth().
			Cursor(mygoui.CursorPointer).
			Label("github.com/jae-jae/tailchat").
			Tooltip(tailchatGitHubURL).
			Children(func() {
				mygoui.Row(c).
					Size(34, 34).
					Radius(10).
					Background(zenSurface).
					Border(1, zenBorder).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Children(func() {
						mygoui.Icon(c, iconGitHub).Size(17, 17).TextColor(zenInk)
					})

				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
					mygoui.Text(c, t.GitHubRepoLabel).FontSize(12.5).Bold().TextColor(zenInk)
					mygoui.Text(c, "github.com/jae-jae/tailchat").
						FontSize(11.5).
						TextColor(zenInkSecondary).
						FillWidth().
						SingleLine()
				})

				mygoui.Icon(c, iconExternalLink).Size(15, 15).TextColor(zenInkMuted)
			})
		if ghBtn.Clicked() {
			if a.openExternalURL != nil {
				if err := a.openExternalURL(tailchatGitHubURL); err != nil {
					a.state.SetToast(t.OpenURLFailPref+err.Error(), true)
				}
			}
		}

		// Footer on Tab 1.
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
	t := tr(a.settingsDraft.SelectedLocale())
	if !a.settingsOpen {
		t = tr(snap.Locale)
	}

	mygoui.DialogBase(c.Key("modal-clear-confirm"), &a.clearConfirmOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(mygoui.RGBA(15, 23, 42, 0.48))
		panel.Width(380).
			MaxWidthPercent(90).
			Padding(20).
			Radius(18).
			Background(zenSurface).
			Border(1, mygoui.RGBA(239, 68, 68, 0.28)).
			Shadow(0, 24, 56, 0, mygoui.RGBA(15, 23, 42, 0.28))

		mygoui.Column(c).Gap(14).FillWidth().Children(func() {
			mygoui.Row(c).Gap(11).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Row(c).
					Size(36, 36).
					Radius(11).
					Background(zenDangerSoft).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Children(func() {
						mygoui.Icon(c, iconAlertTriangle).Size(18, 18).TextColor(zenDanger)
					})
				mygoui.Text(c, t.ClearConfirmTitle).FontSize(15).Bold().TextColor(zenInk).Grow(1).Wrap()
			})

			mygoui.Text(c, t.ClearConfirmBody).
				FontSize(12.5).
				TextColor(zenInkSecondary).
				Wrap().
				FillWidth()

			mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
				if zenPillBtn(c, "clear-confirm-cancel", t.Cancel, false, false).Clicked() {
					a.clearConfirmOpen = false
				}
				if zenDangerSolidPillBtn(c, "clear-confirm-ok", t.ClearConfirmOK).Clicked() {
					err := a.state.ClearAllMessages(context.Background())
					a.clearConfirmOpen = false
					if err == nil {
						a.settingsOpen = false
					}
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
