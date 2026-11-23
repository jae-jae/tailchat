package ui

import (
	"context"
	"fmt"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

func (a *App) renderUpdateBanner(c *mygoui.Context, snap ViewSnapshot) {
	t := tr(snap.Locale)
	up := snap.Update
	if !up.BannerVisible() {
		return
	}

	mygoui.Row(c.Key("update-banner")).
		Padding(7, 14).
		Gap(8).
		AlignItems(mygoui.Center).
		Background(zenAccentSoft).
		BorderWidth(0, 0, 1, 0).
		BorderColor(zenAccentBorder).
		FillWidth().
		Children(func() {
			mygoui.Icon(c, iconSparkles).Size(14, 14).TextColor(zenAccent)

			msgText := fmt.Sprintf(t.UpdateBannerF, up.LatestVersion)
			if up.Installing {
				if up.ProgressTotal > 0 {
					msgText = fmt.Sprintf(t.InstallingUpdatePctF, int(up.Progress()*100))
				} else {
					msgText = t.InstallingUpdate
				}
			}
			mygoui.Text(c, msgText).
				FontSize(12).
				FontWeight(600).
				TextColor(zenAccent).
				Grow(1).
				MinWidth(0).
				SingleLine()

			if !up.Installing {
				if zenPillBtn(c, "banner-view-update", t.ViewUpdate, true, false).Clicked() {
					a.devicesPopoverOpen = false
					a.pairOpen = false
					a.settingsOpen = false
					a.clearConfirmOpen = false
					a.lightboxOpen = false
					a.updateModalOpen = true
				}
				dismissBtn := mygoui.ButtonBase(c.Key("banner-dismiss-update")).
					Size(22, 22).
					Radius(99).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Cursor(mygoui.CursorPointer).
					Label(t.DismissUpdate).
					Tooltip(t.DismissUpdate).
					Children(func() {
						mygoui.Icon(c, iconX).Size(12, 12).TextColor(zenAccent)
					})
				if dismissBtn.Clicked() {
					a.state.DismissUpdate()
				}
			}
		})
}

func (a *App) renderImageBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message, isLocal, showProgress bool, task model.TransferTask, isAnimated bool) {
	t := tr(snap.Locale)
	fb := msg.File

	// Remote large image not yet downloaded locally: show inviting image preview download card.
	if !isLocal || fb.LocalPath == "" {
		a.renderRemoteMediaCard(c, snap, msg, showProgress, task, iconImage, t.DownloadAndPreview)
		return
	}

	const (
		maxW float32 = 280
		maxH float32 = 260
		minW float32 = 136
		minH float32 = 96
	)

	var (
		boxW    float32 = 240
		boxH    float32 = 160
		loading bool
		imgSrc  mygoui.ImageSource
		player  *mygoui.VideoPlayer
	)

	if isAnimated {
		anim := a.media.GetAnimated(fb.BlobHash, fb.LocalPath)
		switch {
		case anim == nil || anim.state == mediaLoading:
			loading = true
		case anim.state == mediaReady:
			boxW, boxH = FitMediaBox(anim.width, anim.height, maxW, maxH, minW, minH)
			player = anim.player
			imgSrc = anim.fallback
		default:
			// Corrupt or unsupported animated format -> fall back to standard file card.
			a.renderStandardFileBubble(c, snap, msg, isLocal, showProgress, task)
			return
		}
	} else {
		entry := a.media.GetImage(fb.BlobHash, fb.LocalPath, chatThumbMaxSide)
		switch {
		case entry == nil || entry.state == mediaLoading:
			loading = true
		case entry.state == mediaReady && entry.src != nil:
			boxW, boxH = FitMediaBox(entry.width, entry.height, maxW, maxH, minW, minH)
			imgSrc = entry.src
		default:
			a.renderStandardFileBubble(c, snap, msg, isLocal, showProgress, task)
			return
		}
	}

	cardW := boxW
	if cardW < 236 {
		cardW = 236
	}

	mygoui.Column(c).
		Width(cardW).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.07)).
		Clip().
		Children(func() {
			// Clickable media canvas (opens Lightbox modal).
			previewBtn := mygoui.ButtonBase(c.Key("img-preview-" + msg.MsgID)).
				FillWidth().
				Height(boxH).
				Background(zenSurfaceSubtle).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Cursor(mygoui.CursorPointer).
				Label(t.PreviewImage).
				Tooltip(t.ClickToZoomHint).
				Clip().
				Children(func() {
					switch {
					case loading:
						mygoui.Column(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
							mygoui.Icon(c, iconImage).Size(24, 24).TextColor(zenInkMuted)
							mygoui.Text(c, t.LoadingPreview).FontSize(11.5).TextColor(zenInkMuted)
						})
					case player != nil:
						mygoui.Video(c, player).Size(boxW, boxH).Fit(mygoui.Contain)
					case imgSrc != nil:
						mygoui.Image(c, imgSrc).Size(boxW, boxH).Fit(mygoui.Contain)
					}

					if isAnimated {
						mygoui.Row(c).
							Absolute().
							Top(8).
							Left(8).
							Padding(2, 7).
							Radius(99).
							Background(mygoui.RGBA(15, 23, 42, 0.68)).
							Children(func() {
								mygoui.Text(c, t.MediaBadgeGIF).FontSize(10).Bold().TextColor(zenSelfBubbleInk)
							})
					}
				})
			if previewBtn.Clicked() {
				a.openLightbox(msg)
			}

			// Compact caption footer: clickable filename opens directly, micro button reveals in folder.
			mygoui.Row(c).
				Padding(6, 10).
				Gap(8).
				AlignItems(mygoui.Center).
				FillWidth().
				Background(zenSurface).
				BorderWidth(1, 0, 0, 0).
				BorderColor(zenBorder).
				Children(func() {
					nameBtn := mygoui.ButtonBase(c.Key("open-img-direct-" + msg.MsgID))
					nameBg := mygoui.RGBA(0, 0, 0, 0)
					if nameBtn.Pressed() {
						nameBg = zenInputFill
					} else if nameBtn.Hovered() {
						nameBg = zenSurfaceSubtle
					}
					nameBtn.Grow(1).
						MinWidth(0).
						Clip().
						Padding(2, 4).
						Radius(6).
						Background(nameBg).
						Justify(mygoui.Start).
						Cursor(mygoui.CursorPointer).
						Label(t.OpenFileDirectly).
						Tooltip(fb.FileName + "\n" + t.OpenFileDirectly).
						Children(func() {
							mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(1).Children(func() {
								mygoui.Text(c, fb.FileName).
									FontSize(11.5).
									FontWeight(600).
									TextColor(zenInk).
									FillWidth().
									SingleLine()
								mygoui.Text(c, FormatBytes(fb.FileSize)).
									FontSize(10.5).
									TextColor(zenInkMuted).
									FillWidth().
									SingleLine()
							})
						})
					if nameBtn.Clicked() {
						a.openFileDirectly(fb)
					}

					if zenMicroBtn(c, "reveal-img-"+msg.MsgID, iconFolderOpen, t.ShowInFolder, false).Clicked() {
						a.revealFileInManager(fb)
					}
				})
		})
}

func (a *App) renderVideoBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message, isLocal, showProgress bool, task model.TransferTask) {
	t := tr(snap.Locale)
	fb := msg.File

	if !isLocal || fb.LocalPath == "" {
		a.renderRemoteMediaCard(c, snap, msg, showProgress, task, iconVideo, t.DownloadAndPreview)
		return
	}

	const (
		cardW   float32 = 280
		posterH float32 = 162
	)

	poster := a.media.GetVideoPoster(fb.BlobHash, fb.LocalPath)
	var posterSrc mygoui.ImageSource
	imgH := posterH
	if poster != nil && poster.state == mediaReady && poster.src != nil {
		posterSrc = poster.src
		_, imgH = FitMediaBox(poster.width, poster.height, cardW, 200, 180, 120)
	}

	mygoui.Column(c).
		Width(cardW).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.08)).
		Clip().
		Children(func() {
			playArea := mygoui.ButtonBase(c.Key("vid-play-" + msg.MsgID)).
				FillWidth().
				Height(imgH).
				Background(mygoui.Hex("#0F172A")).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Cursor(mygoui.CursorPointer).
				Label(t.PlayVideo).
				Tooltip(t.PlayVideo).
				Clip().
				Children(func() {
					if posterSrc != nil {
						mygoui.Image(c, posterSrc).
							Absolute().
							Top(0).
							Left(0).
							Size(cardW, imgH).
							Fit(mygoui.Cover)
					}

					// Subtle dark scrim for Play button readability.
					mygoui.Box(c).
						Absolute().
						Top(0).Left(0).Right(0).Bottom(0).
						Background(mygoui.RGBA(15, 23, 42, 0.28))

					// Top-left VIDEO badge.
					mygoui.Row(c).
						Absolute().
						Top(8).
						Left(8).
						Padding(3, 8).
						Gap(4).
						Radius(99).
						AlignItems(mygoui.Center).
						Background(mygoui.RGBA(15, 23, 42, 0.72)).
						Children(func() {
							mygoui.Icon(c, iconVideo).Size(11, 11).TextColor(zenSelfBubbleInk)
							mygoui.Text(c, t.MediaBadgeVideo).FontSize(10).Bold().TextColor(zenSelfBubbleInk)
						})

					// Centered circular Play emblem.
					mygoui.Row(c).
						Size(46, 46).
						Radius(99).
						Background(mygoui.RGBA(15, 23, 42, 0.72)).
						Border(1.5, mygoui.RGBA(255, 255, 255, 0.72)).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Children(func() {
							mygoui.Icon(c, iconPlay).Size(20, 20).TextColor(zenSelfBubbleInk)
						})
				})

			if playArea.Clicked() {
				a.openFileDirectly(fb)
			}

			// Caption footer: clickable filename opens directly, compact micro button reveals in folder.
			mygoui.Row(c).
				Padding(6, 10).
				Gap(8).
				AlignItems(mygoui.Center).
				FillWidth().
				Background(zenSurface).
				BorderWidth(1, 0, 0, 0).
				BorderColor(zenBorder).
				Children(func() {
					nameBtn := mygoui.ButtonBase(c.Key("open-vid-direct-" + msg.MsgID))
					nameBg := mygoui.RGBA(0, 0, 0, 0)
					if nameBtn.Pressed() {
						nameBg = zenInputFill
					} else if nameBtn.Hovered() {
						nameBg = zenSurfaceSubtle
					}
					nameBtn.Grow(1).
						MinWidth(0).
						Clip().
						Padding(2, 4).
						Radius(6).
						Background(nameBg).
						Justify(mygoui.Start).
						Cursor(mygoui.CursorPointer).
						Label(t.OpenFileDirectly).
						Tooltip(fb.FileName + "\n" + t.OpenFileDirectly).
						Children(func() {
							mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(1).Children(func() {
								mygoui.Text(c, fb.FileName).
									FontSize(12).
									FontWeight(600).
									TextColor(zenInk).
									FillWidth().
									SingleLine()
								mygoui.Text(c, FormatBytes(fb.FileSize)).
									FontSize(10.5).
									TextColor(zenInkMuted).
									FillWidth().
									SingleLine()
							})
						})
					if nameBtn.Clicked() {
						a.openFileDirectly(fb)
					}

					if zenMicroBtn(c, "reveal-vid-"+msg.MsgID, iconFolderOpen, t.ShowInFolder, false).Clicked() {
						a.revealFileInManager(fb)
					}
				})
		})
}

func (a *App) renderAudioBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message, isLocal, showProgress bool, task model.TransferTask) {
	t := tr(snap.Locale)
	fb := msg.File

	if !isLocal || fb.LocalPath == "" {
		a.renderRemoteMediaCard(c, snap, msg, showProgress, task, iconMusic, t.DownloadAndPreview)
		return
	}

	aud := a.audio.Snapshot(msg.MsgID)
	var pct float32
	if aud.Duration > 0 {
		pct = float32(aud.Position) / float32(aud.Duration)
		if pct < 0 {
			pct = 0
		}
		if pct > 1 {
			pct = 1
		}
	}

	timeLabel := FormatBytes(fb.FileSize)
	if aud.Duration > 0 {
		timeLabel = fmt.Sprintf("%s / %s · %s", FormatAudioDuration(aud.Position), FormatAudioDuration(aud.Duration), FormatBytes(fb.FileSize))
	} else if aud.Playing {
		timeLabel = fmt.Sprintf("%s · %s", FormatAudioDuration(aud.Position), FormatBytes(fb.FileSize))
	}

	mygoui.Column(c).
		Width(332).
		Padding(11, 13).
		Gap(9).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.06)).
		Clip().
		Children(func() {
			mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).FillWidth().Children(func() {
				playLabel := t.PlayAudio
				playIcon := iconPlay
				if aud.Playing {
					playLabel = t.PauseAudio
					playIcon = iconPause
				}
				playBtn := mygoui.ButtonBase(c.Key("aud-toggle-" + msg.MsgID))
				btnBg := zenAccent
				if playBtn.Pressed() {
					btnBg = zenAccentPressed
				} else if playBtn.Hovered() {
					btnBg = zenAccentHover
				}
				playBtn.Size(38, 38).
					Radius(99).
					Background(btnBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Cursor(mygoui.CursorPointer).
					Label(playLabel).
					Tooltip(playLabel).
					Children(func() {
						mygoui.Icon(c, playIcon).Size(16, 16).TextColor(zenSelfBubbleInk)
					})
				if playBtn.Clicked() {
					msgID := msg.MsgID
					localPath := fb.LocalPath
					a.dispatchAsync(func() {
						if err := a.audio.Toggle(msgID, localPath); err != nil {
							a.state.SetToast(tr(a.state.Locale()).AudioPlayFailPref+err.Error(), true)
						}
					})
				}

				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(4).Children(func() {
					titleBtn := mygoui.ButtonBase(c.Key("open-aud-direct-" + msg.MsgID))
					titleBg := mygoui.RGBA(0, 0, 0, 0)
					if titleBtn.Pressed() {
						titleBg = zenInputFill
					} else if titleBtn.Hovered() {
						titleBg = zenSurfaceSubtle
					}
					titleBtn.Padding(1, 3).
						Radius(5).
						Background(titleBg).
						Justify(mygoui.Start).
						FillWidth().
						MinWidth(0).
						Clip().
						Cursor(mygoui.CursorPointer).
						Label(t.OpenFileDirectly).
						Tooltip(fb.FileName + "\n" + t.OpenFileDirectly).
						Children(func() {
							mygoui.Text(c, fb.FileName).
								FontSize(12.5).
								FontWeight(600).
								TextColor(zenInk).
								Grow(1).
								MinWidth(0).
								SingleLine()
						})
					if titleBtn.Clicked() {
						a.openFileDirectly(fb)
					}

					// Sleek audio playback track.
					mygoui.Row(c).
						Height(5).
						FillWidth().
						Radius(99).
						Background(zenProgressTrack).
						Clip().
						Children(func() {
							mygoui.Box(c).
								Height(5).
								WidthPercent(pct * 100).
								Radius(99).
								Background(zenAccent)
						})

					mygoui.Text(c, timeLabel).
						FontSize(10.5).
						TextColor(zenInkMuted).
						FillWidth().
						SingleLine()
				})

				if zenMicroBtn(c, "reveal-aud-"+msg.MsgID, iconFolderOpen, t.ShowInFolder, false).Clicked() {
					a.revealFileInManager(fb)
				}
			})
		})
}

func (a *App) renderRemoteMediaCard(c *mygoui.Context, snap ViewSnapshot, msg model.Message, showProgress bool, task model.TransferTask, mediaIcon *mygoui.SVG, downloadCTA string) {
	t := tr(snap.Locale)
	fb := msg.File
	_, hasTask := snap.Transfers[fb.BlobHash]

	var meta string
	switch {
	case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
		meta = fmt.Sprintf("%s / %s · %d%%", FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal), int(task.Progress()*100))
	case hasTask && task.State == model.TransferPaused:
		meta = fmt.Sprintf(t.TransferPausedF, FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal))
	case hasTask && task.State == model.TransferFailed:
		meta = fmt.Sprintf(t.TransferFailedF, FormatBytes(fb.FileSize))
	default:
		meta = formatSeedersMeta(snap.Locale, FormatBytes(fb.FileSize), len(fb.Seeders))
	}

	mygoui.Column(c).
		Width(316).
		Padding(12, 14).
		Gap(10).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.06)).
		Clip().
		Children(func() {
			mygoui.Row(c).Gap(11).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Row(c).
					Size(44, 44).
					Radius(12).
					Background(zenAccentSoft).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Children(func() {
						mygoui.Icon(c, mediaIcon).Size(20, 20).TextColor(zenAccent)
					})
				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Tooltip(fb.FileName).Children(func() {
					mygoui.Text(c, fb.FileName).FontSize(13).FontWeight(600).TextColor(zenInk).FillWidth().SingleLine()
					mygoui.Text(c, meta).FontSize(11.5).TextColor(zenInkSecondary).FillWidth().SingleLine()
				})
			})

			if showProgress {
				pct := task.Progress()
				if pct < 0 {
					pct = 0
				}
				if pct > 1 {
					pct = 1
				}
				barColor := zenAccent
				if task.State == model.TransferPaused {
					barColor = zenInkMuted
				} else if task.State == model.TransferFailed {
					barColor = zenDanger
				}
				mygoui.Row(c).Height(6).FillWidth().Radius(99).Background(zenProgressTrack).Clip().Children(func() {
					mygoui.Box(c).Height(6).WidthPercent(float32(pct * 100)).Radius(99).Background(barColor)
				})
			}

			mygoui.Row(c).Gap(6).Justify(mygoui.End).FillWidth().Children(func() {
				blobHash := fb.BlobHash
				fileSize := fb.FileSize
				switch {
				case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
					if zenPillBtn(c, "pause-m-"+msg.MsgID, t.Pause, false, false).Clicked() {
						a.pauseTransfer(blobHash)
					}
					if zenPillBtn(c, "cancel-m-"+msg.MsgID, t.Cancel, false, false).Clicked() {
						a.cancelTransfer(blobHash)
					}
				case hasTask && task.State == model.TransferPaused:
					if zenPillBtn(c, "resume-m-"+msg.MsgID, t.Resume, true, false).Clicked() {
						a.startDownload(blobHash, fileSize)
					}
					if zenPillBtn(c, "cancel-m-"+msg.MsgID, t.Cancel, false, false).Clicked() {
						a.cancelTransfer(blobHash)
					}
				default:
					if zenPillBtn(c, "dl-m-"+msg.MsgID, downloadCTA, true, false).Clicked() {
						a.startDownload(blobHash, fileSize)
					}
				}
			})
		})
}

func (a *App) openLightbox(msg model.Message) {
	if msg.File == nil || msg.File.LocalPath == "" {
		return
	}
	a.devicesPopoverOpen = false
	a.pairOpen = false
	a.settingsOpen = false
	a.clearConfirmOpen = false
	a.updateModalOpen = false
	a.lightboxMsgID = msg.MsgID
	a.lightboxFile = *msg.File
	a.lightboxOpen = true
}

func (a *App) renderLightboxModal(c *mygoui.Context, snap ViewSnapshot) {
	if !a.lightboxOpen {
		return
	}
	t := tr(snap.Locale)
	fb := a.lightboxFile
	isAnim := ClassifyMedia(fb.FileName, fb.MimeType) == MediaAnimated

	const (
		maxW float32 = 464
		maxH float32 = 500
	)

	var (
		imgSrc  mygoui.ImageSource
		player  *mygoui.VideoPlayer
		boxW    float32 = 340
		boxH    float32 = 240
		dimText string
		loading bool
	)

	if isAnim {
		anim := a.media.GetAnimated("lb-"+fb.BlobHash, fb.LocalPath)
		if anim == nil || anim.state == mediaLoading {
			loading = true
		} else if anim.state == mediaReady {
			boxW, boxH = FitMediaBox(anim.width, anim.height, maxW, maxH, 160, 120)
			player = anim.player
			imgSrc = anim.fallback
			if anim.width > 0 && anim.height > 0 {
				dimText = fmt.Sprintf("%d×%d", int(anim.width), int(anim.height))
			}
		}
	} else {
		entry := a.media.GetImage(fb.BlobHash, fb.LocalPath, lightboxMaxSide)
		if entry == nil || entry.state == mediaLoading {
			loading = true
		} else if entry.state == mediaReady && entry.src != nil {
			boxW, boxH = FitMediaBox(entry.width, entry.height, maxW, maxH, 160, 120)
			imgSrc = entry.src
			if entry.origW > 0 && entry.origH > 0 {
				dimText = fmt.Sprintf("%d×%d", entry.origW, entry.origH)
			}
		}
	}

	mygoui.DialogBase(c.Key("modal-lightbox"), &a.lightboxOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(mygoui.RGBA(2, 6, 23, 0.72))
		panel.Width(500).
			MaxWidthPercent(94).
			Padding(16).
			Radius(18).
			Background(mygoui.Hex("#0F172A")).
			Border(1, mygoui.RGBA(255, 255, 255, 0.14)).
			Shadow(0, 20, 50, 0, mygoui.RGBA(2, 6, 23, 0.55))

		mygoui.Column(c).
			Gap(12).
			FillWidth().
			Children(func() {
				// Top header bar: filename + dimensions + Open, Show in Folder & Close buttons.
				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).FillWidth().Children(func() {
					mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Tooltip(fb.FileName).Children(func() {
						mygoui.Text(c, fb.FileName).
							FontSize(13.5).
							Bold().
							TextColor(zenSelfBubbleInk).
							FillWidth().
							SingleLine()
						sub := FormatBytes(fb.FileSize)
						if dimText != "" {
							sub = dimText + " · " + sub
						}
						mygoui.Text(c, sub).
							FontSize(11).
							TextColor(mygoui.RGBA(203, 213, 225, 0.85)).
							FillWidth().
							SingleLine()
					})

					if zenPillBtn(c, "lb-open-file", t.OpenFile, false, false).Clicked() {
						a.openFileDirectly(&fb)
					}

					if zenIconPillBtn(c, "lb-reveal-file", iconFolderOpen, t.ShowInFolder, false, false).Clicked() {
						a.revealFileInManager(&fb)
					}

					closeBtn := mygoui.ButtonBase(c.Key("lb-close")).
						Size(30, 30).
						Radius(99).
						Background(mygoui.RGBA(255, 255, 255, 0.12)).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Shrink(0).
						Cursor(mygoui.CursorPointer).
						Label(t.ClosePreview).
						Tooltip(t.ClosePreview).
						Children(func() {
							mygoui.Icon(c, iconX).Size(15, 15).TextColor(zenSelfBubbleInk)
						})
					if closeBtn.Clicked() {
						a.lightboxOpen = false
					}
				})

				// Image display stage.
				mygoui.Row(c).
					FillWidth().
					Height(boxH).
					Radius(12).
					Background(mygoui.RGBA(2, 6, 23, 0.45)).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Clip().
					Children(func() {
						switch {
						case loading:
							mygoui.Text(c, t.LoadingPreview).FontSize(12.5).TextColor(mygoui.RGBA(203, 213, 225, 0.9))
						case player != nil:
							mygoui.Video(c, player).Size(boxW, boxH).Fit(mygoui.Contain)
						case imgSrc != nil:
							mygoui.Image(c, imgSrc).Size(boxW, boxH).Fit(mygoui.Contain)
						}
					})
			})
	})
}

func (a *App) renderUpdateModal(c *mygoui.Context, snap ViewSnapshot) {
	if !a.updateModalOpen {
		return
	}
	t := tr(snap.Locale)
	up := snap.Update

	mygoui.DialogBase(c.Key("modal-update"), &a.updateModalOpen, func(backdrop, panel mygoui.Element) {
		backdrop.Background(mygoui.RGBA(15, 23, 42, 0.34))
		panel.Width(430).
			MaxWidthPercent(92).
			Padding(20).
			Radius(18).
			Background(zenSurface).
			Border(1, zenBorder).
			Shadow(0, 20, 48, 0, mygoui.RGBA(15, 23, 42, 0.22))

		mygoui.Column(c).
			Gap(14).
			FillWidth().
			Children(func() {
				// Header.
				mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).FillWidth().Children(func() {
					mygoui.Row(c).
						Size(34, 34).
						Radius(10).
						Background(zenAccentSoft).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Children(func() {
							mygoui.Icon(c, iconSparkles).Size(17, 17).TextColor(zenAccent)
						})
					mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						mygoui.Text(c, t.UpdateModalTitle).FontSize(15).Bold().TextColor(zenInk)
						if up.LatestVersion != "" {
							mygoui.Text(c, fmt.Sprintf(t.UpdateVersionCompareF, up.CurrentVersion, up.LatestVersion)).
								FontSize(12).
								FontWeight(600).
								TextColor(zenAccent)
						} else {
							mygoui.Text(c, fmt.Sprintf(t.CurrentVersionF, up.CurrentVersion)).
								FontSize(12).
								TextColor(zenInkSecondary)
						}
					})
					if zenCircleBtn(c, "update-modal-close", iconX, t.Close, false).Clicked() {
						a.updateModalOpen = false
					}
				})

				// Release notes box.
				notes := up.Notes
				if notes == "" {
					notes = t.NoReleaseNotes
				}
				mygoui.Column(c).Gap(6).FillWidth().Children(func() {
					mygoui.Text(c, t.ReleaseNotesLabel).FontSize(12).FontWeight(600).TextColor(zenInk)
					mygoui.Scroll(c.Key("update-notes-scroll")).
						Height(160).
						FillWidth().
						Radius(12).
						Background(zenSurfaceSubtle).
						Border(1, zenBorder).
						Padding(12).
						Children(func() {
							mygoui.Text(c, notes).
								FontSize(12).
								TextColor(zenInkSecondary).
								Wrap().
								Selectable()
						})
				})

				// Progress bar when installing native signed update.
				if up.Installing {
					pct := up.Progress()
					statusText := t.InstallingUpdate
					if up.ProgressTotal > 0 {
						statusText = fmt.Sprintf(t.InstallingUpdatePctF, int(pct*100))
					}
					mygoui.Column(c).Gap(6).FillWidth().Children(func() {
						mygoui.Text(c, statusText).FontSize(12).FontWeight(600).TextColor(zenAccent)
						mygoui.Row(c).Height(6).FillWidth().Radius(99).Background(zenProgressTrack).Clip().Children(func() {
							mygoui.Box(c).Height(6).WidthPercent(float32(pct * 100)).Radius(99).Background(zenAccent)
						})
					})
				}

				if up.LastError != "" {
					mygoui.Text(c, up.LastError).FontSize(12).TextColor(zenDanger).Wrap()
				}

				// Footer CTA buttons.
				mygoui.Row(c).Gap(8).Justify(mygoui.End).FillWidth().Children(func() {
					if zenPillBtn(c, "update-btn-later", t.Close, false, up.Installing).Clicked() {
						a.updateModalOpen = false
					}
					if up.CanAutoInstall {
						if zenPillBtn(c, "update-btn-install", t.UpdateNow, true, up.Installing).Clicked() {
							a.state.InstallUpdate(context.Background())
						}
					} else if up.ReleaseURL != "" {
						if zenPillBtn(c, "update-btn-download", t.DownloadRelease, true, false).Clicked() {
							a.triggerOpenExternalURL(up.ReleaseURL)
						}
					}
				})
			})
	})
}
