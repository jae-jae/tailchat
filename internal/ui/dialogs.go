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
		return model.AppConfig{}, fmt.Errorf("invalid region ID")
	}
	derpPort, err := parseIntDefault(d.CustomDERPPortStr, 443)
	if err != nil || derpPort <= 0 || derpPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("invalid DERP port")
	}
	stunPort, err := parseIntDefault(d.CustomSTUNPortStr, 3478)
	if err != nil || stunPort <= 0 || stunPort > 65535 {
		return model.AppConfig{}, fmt.Errorf("invalid STUN port")
	}
	autoMB, err := parseInt64Default(d.AutoDownloadMBStr, 20)
	if err != nil || autoMB < 0 {
		return model.AppConfig{}, fmt.Errorf("invalid auto-download threshold (MB)")
	}
	rateKBs, err := parseInt64Default(d.RateLimitKBsStr, 0)
	if err != nil || rateKBs < 0 {
		return model.AppConfig{}, fmt.Errorf("invalid download rate limit (KB/s)")
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
			return model.AppConfig{}, fmt.Errorf("custom derper host is required")
		}
		if derpCfg.RegionID < 900 {
			derpCfg.RegionID = 900
		}
	case 2:
		derpCfg.Mode = model.DERPModeCustomMapURL
		if derpCfg.DERPMapURL == "" {
			return model.AppConfig{}, fmt.Errorf("custom DERPMap URL is required")
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

func (a *App) renderPairingModal(c *mygoui.Context, snap ViewSnapshot) {
	t := c.Theme()

	mygoui.Modal(c.Key("modal-pair"), &a.pairOpen, func() {
		mygoui.Column(c).Gap(14).Width(500).Padding(4).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "Pair Devices").FontSize(18).Bold()
				mygoui.Spacer(c)
				if mygoui.Button(c.Key("pair-close"), "Close").Clicked() {
					a.pairOpen = false
				}
			})

			mygoui.Divider(c)

			// Host side: generate pairing code.
			mygoui.Column(c).Gap(8).Children(func() {
				mygoui.Text(c, "Add a new device to this group").Bold().FontSize(14)
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					if mygoui.PrimaryButton(c.Key("btn-gen-pair"), "Generate Pairing Code").Clicked() {
						code, tok, err := a.state.CreatePairingCode(context.Background(), 300)
						if err != nil {
							a.pairErr = err.Error()
						} else {
							a.pairErr = ""
							a.pairCode = code
							a.pairToken = &tok
						}
					}
					if a.pairCode != "" {
						if mygoui.Button(c.Key("btn-copy-pair"), "Copy Code").Clicked() {
							_ = a.writeClipboardText(a.pairCode, false)
							a.state.SetToast("Copied pairing code", false)
						}
						if mygoui.Button(c.Key("btn-cancel-pair"), "Cancel Code").Clicked() {
							_ = a.state.CancelPairing()
							a.pairCode = ""
							a.pairToken = nil
						}
					}
				})

				if a.pairCode != "" {
					mygoui.Column(c).
						Padding(10).
						Gap(6).
						Radius(8).
						Background(t.Surface).
						Border(1, t.Border).
						Children(func() {
							mygoui.Text(c, a.pairCode).Wrap().Selectable().FontSize(12)
							if a.pairToken != nil && a.pairToken.ExpiresAt > 0 {
								rem := time.Until(time.UnixMilli(a.pairToken.ExpiresAt))
								if rem > 0 {
									c.After(time.Second)
									mins := int(rem.Minutes())
									secs := int(rem.Seconds()) % 60
									mygoui.Text(c, fmt.Sprintf("Expires in %dm %02ds", mins, secs)).FontSize(11).TextColor(t.TextMuted)
								} else {
									mygoui.Text(c, "Pairing code expired — generate a new one").FontSize(11).TextColor(t.Danger)
								}
							}
						})
				}
			})

			mygoui.Divider(c)

			// Join side: paste pairing code.
			mygoui.Column(c).Gap(8).Children(func() {
				mygoui.Text(c, "Join Existing Group").Bold().FontSize(14)
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					in := mygoui.TextInput(c.Key("join-code-input"), &a.joinCodeInput).
						Placeholder("Paste tailchat-pair://... code").
						Label("Pairing code").
						Grow(1)
					joinClicked := mygoui.PrimaryButton(c.Key("btn-join-group"), "Join").
						Disabled(strings.TrimSpace(a.joinCodeInput) == "").
						Clicked()
					if (joinClicked || in.Submitted()) && strings.TrimSpace(a.joinCodeInput) != "" {
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

			if a.pairErr != "" {
				mygoui.Text(c, a.pairErr).FontSize(12).TextColor(t.Danger).Wrap()
			}
		})
	})
}

func (a *App) renderDevicesModal(c *mygoui.Context, snap ViewSnapshot) {
	t := c.Theme()

	mygoui.Modal(c.Key("modal-devices"), &a.devicesOpen, func() {
		mygoui.Column(c).Gap(12).Width(560).Padding(4).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "Paired Devices").FontSize(18).Bold()
				mygoui.Spacer(c)
				if mygoui.Button(c.Key("devices-close"), "Close").Clicked() {
					a.devicesOpen = false
				}
			})

			mygoui.Divider(c)

			if len(snap.Devices) == 0 {
				mygoui.Text(c, "No devices in group yet.").TextColor(t.TextMuted)
			} else {
				mygoui.Column(c).Gap(8).FillWidth().Children(func() {
					for _, dev := range snap.Devices {
						a.renderDeviceRow(c, snap, dev)
					}
				})
			}

			if a.devicesErr != "" {
				mygoui.Text(c, a.devicesErr).FontSize(12).TextColor(t.Danger).Wrap()
			}
		})
	})
}

func (a *App) renderDeviceRow(c *mygoui.Context, snap ViewSnapshot, dev model.Device) {
	t := c.Theme()
	isSelf := dev.IsSelf || (snap.Identity.DeviceID != "" && dev.DeviceID == snap.Identity.DeviceID)
	peer, hasPeer := snap.Peers[dev.DeviceID]

	title := dev.Name
	if title == "" {
		title = shortID(dev.DeviceID)
	}
	if isSelf {
		title += " (This device)"
	}

	pathText := "Offline"
	pathBg := t.Border
	pathFg := t.TextMuted
	detail := fmt.Sprintf("Platform: %s · Epoch #%d", dev.Platform, dev.RouteEpoch)

	if isSelf {
		pathText = "Local"
		pathBg = t.Accent.Alpha(0.16)
		pathFg = t.Accent
	} else if hasPeer && peer.Connected {
		switch peer.PathKind {
		case model.ConnPathDirect:
			pathText = "Direct"
			pathBg = t.Success.Alpha(0.18)
			pathFg = t.Success
			if peer.Endpoint != "" {
				detail += " · " + peer.Endpoint
			}
		case model.ConnPathDERP:
			pathText = "DERP"
			pathBg = t.Warning.Alpha(0.20)
			pathFg = t.Warning
			if peer.DERPRegion != "" {
				detail += " · Region " + peer.DERPRegion
			}
		default:
			pathText = string(peer.PathKind)
		}
		if peer.LatencyMs > 0 {
			detail += fmt.Sprintf(" · %.1f ms", peer.LatencyMs)
		}
	} else if hasPeer && peer.PathKind == model.ConnPathConnecting {
		pathText = "Connecting"
	}

	mygoui.Row(c.Key("dev-"+dev.DeviceID)).
		Padding(10, 12).
		Gap(10).
		Radius(8).
		Background(t.Surface).
		Border(1, t.Border).
		AlignItems(mygoui.Center).
		FillWidth().
		Children(func() {
			mygoui.Column(c).Grow(1).Gap(4).Children(func() {
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					mygoui.Text(c, title).Bold().FontSize(14)
					mygoui.Row(c).Padding(2, 8).Radius(10).Background(pathBg).Children(func() {
						mygoui.Text(c, pathText).FontSize(11).Bold().TextColor(pathFg)
					})
				})
				mygoui.Text(c, detail).FontSize(12).TextColor(t.TextMuted)
			})

			if !isSelf {
				if mygoui.Button(c.Key("revoke-"+dev.DeviceID), "Revoke").Clicked() {
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
	t := c.Theme()

	mygoui.Modal(c.Key("modal-settings"), &a.settingsOpen, func() {
		mygoui.Column(c).Gap(12).Width(560).Padding(4).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "Settings").FontSize(18).Bold()
				mygoui.Spacer(c)
				if mygoui.Button(c.Key("settings-close"), "Close").Clicked() {
					a.settingsOpen = false
				}
			})

			mygoui.Divider(c)

			// Device Name
			mygoui.Column(c).Gap(4).Children(func() {
				mygoui.Text(c, "Device Name").FontSize(12).Bold()
				mygoui.TextInput(c.Key("set-device-name"), &a.settingsDraft.DeviceName).
					Placeholder("My Device").
					Label("Device Name").
					FillWidth()
			})

			// DERP Relay Mode
			mygoui.Column(c).Gap(6).Children(func() {
				mygoui.Text(c, "DERP Relay Server").FontSize(12).Bold()
				mygoui.Segmented(c.Key("set-derp-mode"), &a.settingsDraft.DERPModeIdx,
					"Default tailcat.dev",
					"Custom Self-Hosted derper",
					"Custom DERPMap URL",
				).Label("DERP Mode").FillEqually()

				switch a.settingsDraft.DERPModeIdx {
				case 0:
					mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
						mygoui.Text(c, "Preferred Region ID").FontSize(12).TextColor(t.TextMuted)
						mygoui.TextInput(c.Key("set-region-id"), &a.settingsDraft.RegionIDStr).
							Placeholder("304").
							Label("Region ID").
							Width(120)
					})
				case 1:
					mygoui.Column(c).Gap(6).Children(func() {
						mygoui.TextInput(c.Key("set-custom-host"), &a.settingsDraft.CustomHost).
							Placeholder("derp.example.com").
							Label("Custom DERP Host").
							FillWidth()
						mygoui.Row(c).Gap(8).Children(func() {
							mygoui.TextInput(c.Key("set-derp-port"), &a.settingsDraft.CustomDERPPortStr).
								Placeholder("443").
								Label("DERP Port").
								Grow(1)
							mygoui.TextInput(c.Key("set-stun-port"), &a.settingsDraft.CustomSTUNPortStr).
								Placeholder("3478").
								Label("STUN Port").
								Grow(1)
							mygoui.TextInput(c.Key("set-region-name"), &a.settingsDraft.CustomRegionName).
								Placeholder("Custom Region").
								Label("Region Name").
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
					if mygoui.PrimaryButton(c.Key("btn-hotswitch-derp"), "Apply & Hot-Switch DERP").Clicked() {
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

			mygoui.Divider(c)

			// Transfers & Desktop Preferences
			mygoui.Row(c).Gap(10).Children(func() {
				mygoui.Column(c).Gap(4).Grow(1).Children(func() {
					mygoui.Text(c, "Auto-download max (MB)").FontSize(12).Bold()
					mygoui.TextInput(c.Key("set-autodl-mb"), &a.settingsDraft.AutoDownloadMBStr).
						Placeholder("20").
						Label("Auto-download MB").
						FillWidth()
				})
				mygoui.Column(c).Gap(4).Grow(1).Children(func() {
					mygoui.Text(c, "Rate limit (KB/s, 0=unlimited)").FontSize(12).Bold()
					mygoui.TextInput(c.Key("set-rate-kbs"), &a.settingsDraft.RateLimitKBsStr).
						Placeholder("0").
						Label("Download rate limit").
						FillWidth()
				})
			})

			mygoui.Checkbox(c.Key("set-min-tray"), &a.settingsDraft.MinimizeToTray, "Minimize to system tray on close")

			if a.settingsErr != "" {
				mygoui.Text(c, a.settingsErr).FontSize(12).TextColor(t.Danger).Wrap()
			}

			mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
				if mygoui.Button(c.Key("btn-save-settings"), "Save Settings").Clicked() {
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
