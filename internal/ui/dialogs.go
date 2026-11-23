package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

// SettingsDraft holds editable form fields for the Settings modal.
type SettingsDraft struct {
	DeviceName        string
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

// NewSettingsDraft initializes a SettingsDraft from an existing AppConfig.
func NewSettingsDraft(cfg model.AppConfig) SettingsDraft {
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
		DownloadDir:       cfg.DownloadDir,
	}
}

// ToConfig validates and converts the draft back into a model.AppConfig.
func (d SettingsDraft) ToConfig() (model.AppConfig, error) {
	regionID, err := parseIntDefault(d.RegionIDStr, 304)
	if err != nil || regionID < 0 {
		return model.AppConfig{}, fmt.Errorf("Region ID 无效")
	}
	derpPort, err := parseIntDefault(d.CustomDERPPortStr, 443)
	if err != nil || derpPort <= 0 || derpPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("DERP 端口无效")
	}
	stunPort, err := parseIntDefault(d.CustomSTUNPortStr, 3478)
	if err != nil || stunPort <= 0 || stunPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("STUN 端口无效")
	}
	autoMB, err := parseInt64Default(d.AutoDownloadMBStr, 20)
	if err != nil || autoMB < 0 {
		return model.AppConfig{}, fmt.Errorf("自动下载阈值 (MB) 无效")
	}
	rateKBs, err := parseInt64Default(d.RateLimitKBsStr, 0)
	if err != nil || rateKBs < 0 {
		return model.AppConfig{}, fmt.Errorf("下载限速 (KB/s) 无效")
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
			return model.AppConfig{}, fmt.Errorf("请输入自建 derper 服务器域名或 IP")
		}
		if derpCfg.RegionID < 900 {
			derpCfg.RegionID = 900
		}
	case 2:
		derpCfg.Mode = model.DERPModeCustomMapURL
		if derpCfg.DERPMapURL == "" {
			return model.AppConfig{}, fmt.Errorf("请输入自定义 DERPMap URL")
		}
	default:
		derpCfg.Mode = model.DERPModeDefault
	}

	return model.AppConfig{
		DeviceName:           strings.TrimSpace(d.DeviceName),
		DERP:                 derpCfg,
		AutoDownloadMaxBytes: autoMB * 1024 * 1024,
		MaxDownloadBytesPerS: rateKBs * 1024,
		DownloadDir:          d.DownloadDir,
		MinimizeToTray:       d.MinimizeToTray,
		GlobalHotkey:         strings.TrimSpace(d.GlobalHotkey),
	}, nil
}

// renderDevicesPopover renders the lightweight Devices-only popover anchored to the header button.
func (a *App) renderDevicesPopover(c *mygoui.Context, anchor mygoui.Element, snap ViewSnapshot) {
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
				mygoui.Text(c, "已配对设备").FontSize(13.5).Bold().TextColor(zenInk)
				mygoui.Spacer(c)

				badgeBg := zenInputFill
				badgeFg := zenInkMuted
				badgeText := "仅本机"
				if snap.OnlineCount > 0 {
					badgeBg = zenDirectSoft
					badgeFg = zenDirectText
					badgeText = fmt.Sprintf("%d 台在线", snap.OnlineCount)
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
				mygoui.Text(c, "暂无已配对设备").FontSize(12).TextColor(zenInkMuted)
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
				Label("配对新设备").
				Children(func() {
					mygoui.Icon(c, iconQRCode).Size(14, 14).TextColor(zenAccent)
					mygoui.Text(c, "配对新设备").FontSize(12).FontWeight(600).TextColor(zenAccent).NoWrap()
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
func (a *App) renderPairingModal(c *mygoui.Context, _ ViewSnapshot) {
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
				mygoui.Text(c, "配对设备").FontSize(16).Bold().TextColor(zenInk)
				mygoui.Spacer(c)
				if zenCircleBtn(c, "pair-close", iconX, "关闭", false).Clicked() {
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
					a.renderPairTabBtn(c, "pair-tab-qr", iconQRCode, "扫码配对", a.pairTab == 0, func() {
						a.pairTab = 0
						a.pairErr = ""
						a.ensurePairingCode()
					})
					a.renderPairTabBtn(c, "pair-tab-join", iconLink, "输入配对码加入", a.pairTab == 1, func() {
						a.pairTab = 1
						a.pairErr = ""
					})
				})

			if a.pairTab == 0 {
				a.renderPairingQRTab(c)
			} else {
				a.renderPairingJoinTab(c)
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

func (a *App) renderPairingQRTab(c *mygoui.Context) {
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
							mygoui.Text(c, "配对二维码已过期").FontSize(12.5).Bold().TextColor(zenInk).NoWrap()
							if zenPillBtn(c, "btn-regen-pair", "重新生成", true, false).Clicked() {
								a.refreshPairingCode()
							}
						})
					} else if qrBmp := a.pairingQRBitmap(); qrBmp != nil {
						mygoui.Image(c, qrBmp).Size(176, 176)
					} else {
						mygoui.Text(c, "正在生成二维码…").FontSize(12).TextColor(zenInkMuted)
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
								mygoui.Text(c, fmt.Sprintf("有效剩余 %d:%02d", mins, secs)).
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
							mygoui.Text(c, "已过期").FontSize(11).FontWeight(650).TextColor(zenDanger).NoWrap()
						})
				}
				mygoui.Text(c, "一次配对，全群自动互通").FontSize(12).TextColor(zenInkSecondary).NoWrap()
			})
		})

	// Concise helper caption.
	mygoui.Text(c, "用新设备上的 TailChat 扫码即可加入；若对方无摄像头，可一键复制配对码发送。").
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
		copyText := "一键复制配对码"

		if copyDisabled {
			copyBg = zenInkMuted
		} else if copied {
			copyBg = zenDirectText
			copyIcon = iconCheck
			copyText = "已复制配对码"
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
				a.pairErr = "复制配对码失败：" + err.Error()
			} else {
				a.pairErr = ""
				a.pairCopiedUntil = time.Now().Add(1800 * time.Millisecond)
				a.state.SetToast("配对码已复制到剪贴板", false)
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
			Label("刷新二维码").
			Tooltip("刷新二维码").
			Children(func() {
				mygoui.Icon(c, iconRefreshCw).Size(15, 15).TextColor(refFg)
			})
		if refreshBtn.Clicked() {
			a.refreshPairingCode()
		}
	})
}

func (a *App) renderPairingJoinTab(c *mygoui.Context) {
	mygoui.Column(c).
		Padding(16).
		Gap(12).
		Radius(16).
		Background(zenSurfaceSubtle).
		Border(1, zenBorder).
		FillWidth().
		Children(func() {
			mygoui.Text(c, "加入已有设备群").FontSize(13).Bold().TextColor(zenInk)
			mygoui.Text(c, "在另一台已配对设备上点击「一键复制配对码」，粘贴到下方即可与所有设备建立加密 P2P 同步。").
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
						Placeholder("粘贴 tailchat-pair://…").
						Label("配对码").
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
					Label("从剪贴板粘贴").
					Children(func() {
						mygoui.Icon(c, iconClipboard).Size(14, 14).TextColor(zenInk)
						mygoui.Text(c, "从剪贴板粘贴").FontSize(12.5).FontWeight(600).TextColor(zenInk).NoWrap()
					})
				if pasteBtn.Clicked() {
					txt := ""
					if a.readClipboardText != nil {
						txt = strings.TrimSpace(a.readClipboardText())
					}
					if txt == "" {
						a.state.SetToast("剪贴板为空，请先复制配对码", false)
					} else {
						a.joinCodeInput = txt
						a.pairErr = ""
						a.state.SetToast("已从剪贴板填入配对码", false)
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
					Label("立即加入")
				if canJoin {
					joinBtn.Cursor(mygoui.CursorPointer)
				}
				joinBtn.Children(func() {
					mygoui.Text(c, "立即加入").FontSize(13).FontWeight(600).TextColor(zenSelfBubbleInk).NoWrap()
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
	isSelf := dev.IsSelf || (snap.Identity.DeviceID != "" && dev.DeviceID == snap.Identity.DeviceID)
	peer, hasPeer := snap.Peers[dev.DeviceID]

	title := dev.Name
	if title == "" {
		title = shortID(dev.DeviceID)
	}

	subtitle := "离线"
	subColor := zenInkMuted
	online := false
	statusColor := zenInkMuted

	if isSelf {
		subtitle = "本机"
		subColor = zenAccent
		online = true
		statusColor = zenDirect
	} else if hasPeer && peer.Connected {
		online = true
		switch peer.PathKind {
		case model.ConnPathDirect:
			subtitle = "P2P 直连"
			subColor = zenDirectText
			statusColor = zenDirect
		case model.ConnPathDERP:
			subtitle = "DERP 中继"
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
		subtitle = "正在连接…"
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
			mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mygoui.Text(c, title).FontSize(12.5).Bold().TextColor(zenInk).NoWrap().Ellipsis("…")
				mygoui.Text(c, subtitle).FontSize(11).TextColor(subColor).NoWrap().Ellipsis("…")
			})

			if online {
				mygoui.Icon(c, iconCheckCircle).Size(15, 15).TextColor(statusColor)
			} else {
				mygoui.Icon(c, iconCircleDashed).Size(15, 15).TextColor(zenInkMuted)
			}

			if !isSelf {
				if zenDangerPillBtn(c, "revoke-"+dev.DeviceID, "移除").Clicked() {
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
	mygoui.DialogBase(c.Key("modal-settings"), &a.settingsOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(mygoui.RGBA(15, 23, 42, 0.32))
		panel.Width(440).
			Padding(20).
			Radius(18).
			Background(zenSurface).
			Border(1, zenBorder).
			Shadow(0, 20, 48, 0, mygoui.RGBA(15, 23, 42, 0.22))

		mygoui.Column(c).Gap(12).FillWidth().Children(func() {
			// Modal header.
			mygoui.Row(c).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Text(c, "设置与 DERP").FontSize(16).Bold().TextColor(zenInk)
				mygoui.Spacer(c)
				if zenCircleBtn(c, "settings-x", iconX, "关闭设置", false).Clicked() {
					a.settingsOpen = false
				}
			})

			mygoui.Box(c).Height(1).FillWidth().Background(zenBorder)

			// Device Name.
			mygoui.Column(c).Gap(5).FillWidth().Children(func() {
				mygoui.Text(c, "设备名称").FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.TextInput(c.Key("set-device-name"), &a.settingsDraft.DeviceName).
					Placeholder("我的设备").
					Label("设备名称").
					FillWidth()
			})

			// DERP Relay Server.
			mygoui.Column(c).Gap(7).FillWidth().Children(func() {
				mygoui.Text(c, "DERP 中继服务器").FontSize(12).FontWeight(600).TextColor(zenInk)
				mygoui.Segmented(c.Key("set-derp-mode"), &a.settingsDraft.DERPModeIdx,
					"默认 tailcat",
					"自建 derper",
					"自定义 DERPMap",
				).Label("DERP 模式").FillEqually()

				switch a.settingsDraft.DERPModeIdx {
				case 0:
					mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
						mygoui.Text(c, "首选 Region ID").FontSize(12).TextColor(zenInkSecondary)
						mygoui.TextInput(c.Key("set-region-id"), &a.settingsDraft.RegionIDStr).
							Placeholder("304").
							Label("Region ID").
							Width(120)
					})
				case 1:
					mygoui.Column(c).Gap(6).FillWidth().Children(func() {
						mygoui.TextInput(c.Key("set-custom-host"), &a.settingsDraft.CustomHost).
							Placeholder("derp.example.com").
							Label("自建 DERP 域名").
							FillWidth()
						mygoui.Row(c).Gap(6).FillWidth().Children(func() {
							mygoui.TextInput(c.Key("set-derp-port"), &a.settingsDraft.CustomDERPPortStr).
								Placeholder("443").
								Label("DERP 端口").
								Grow(1)
							mygoui.TextInput(c.Key("set-stun-port"), &a.settingsDraft.CustomSTUNPortStr).
								Placeholder("3478").
								Label("STUN 端口").
								Grow(1)
							mygoui.TextInput(c.Key("set-region-name"), &a.settingsDraft.CustomRegionName).
								Placeholder("自建节点").
								Label("区域名称").
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
					if zenPillBtn(c, "btn-hotswitch-derp", "应用并热切 DERP", true, false).Clicked() {
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

			// Transfers & Desktop Preferences.
			mygoui.Row(c).Gap(10).FillWidth().Children(func() {
				mygoui.Column(c).Gap(5).Grow(1).Children(func() {
					mygoui.Text(c, "自动下载阈值 (MB)").FontSize(12).FontWeight(600).TextColor(zenInk)
					mygoui.TextInput(c.Key("set-autodl-mb"), &a.settingsDraft.AutoDownloadMBStr).
						Placeholder("20").
						Label("自动下载阈值 (MB)").
						FillWidth()
				})
				mygoui.Column(c).Gap(5).Grow(1).Children(func() {
					mygoui.Text(c, "下载限速 (KB/s)").FontSize(12).FontWeight(600).TextColor(zenInk)
					mygoui.TextInput(c.Key("set-rate-kbs"), &a.settingsDraft.RateLimitKBsStr).
						Placeholder("0").
						Label("下载限速 (KB/s)").
						FillWidth()
				})
			})

			mygoui.Checkbox(c.Key("set-min-tray"), &a.settingsDraft.MinimizeToTray, "关闭窗口时最小化到托盘")

			if a.settingsErr != "" {
				mygoui.Text(c, a.settingsErr).FontSize(12).TextColor(zenDanger).Wrap()
			}

			// Footer actions.
			mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
				if zenPillBtn(c, "settings-close", "关闭", false, false).Clicked() {
					a.settingsOpen = false
				}
				if zenPillBtn(c, "btn-save-settings", "保存设置", true, false).Clicked() {
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
