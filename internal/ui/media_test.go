package ui

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
	"tailchat/internal/updater"
)

func TestClassifyMedia(t *testing.T) {
	cases := []struct {
		name string
		mime string
		want MediaKind
	}{
		{"photo.png", "", MediaImage},
		{"IMG_2026.JPG", "", MediaImage},
		{"shot.jpeg", "", MediaImage},
		{"banner.webp", "", MediaImage},
		{"bitmap.bmp", "", MediaImage},
		{"diagram.svg", "", MediaImage},
		{"party.gif", "", MediaAnimated},
		{"sticker.apng", "", MediaAnimated},
		{"voice.mp3", "", MediaAudio},
		{"memo.wav", "", MediaAudio},
		{"clip.ogg", "", MediaAudio},
		{"track.flac", "", MediaAudio},
		{"note.m4a", "", MediaAudio},
		{"demo.mp4", "", MediaVideo},
		{"screen.mov", "", MediaVideo},
		{"movie.mkv", "", MediaVideo},
		{"capture.webm", "", MediaVideo},
		{"design-spec.pdf", "application/pdf", MediaOther},
		{"archive.zip", "application/zip", MediaOther},
		// MIME fallback when filename has no extension:
		{"blob-1", "image/png", MediaImage},
		{"blob-2", "image/gif", MediaAnimated},
		{"blob-3", "audio/mpeg", MediaAudio},
		{"blob-4", "video/mp4", MediaVideo},
	}

	for _, tc := range cases {
		if got := ClassifyMedia(tc.name, tc.mime); got != tc.want {
			t.Errorf("ClassifyMedia(%q, %q) = %v, want %v", tc.name, tc.mime, got, tc.want)
		}
	}
}

func TestFitMediaBox(t *testing.T) {
	// Landscape 1600x900 into max 280x260 -> width clamped to 280, height = 157.5
	w, h := FitMediaBox(1600, 900, 280, 260, 136, 96)
	if w < 279 || w > 281 || h < 156 || h > 159 {
		t.Fatalf("landscape fit unexpected: got %.1fx%.1f", w, h)
	}

	// Portrait 600x1200 into max 280x260 -> height clamped to 260, width = 136 (minW)
	w, h = FitMediaBox(600, 1200, 280, 260, 136, 96)
	if h < 259 || h > 261 || w < 136 {
		t.Fatalf("portrait fit unexpected: got %.1fx%.1f", w, h)
	}

	// Zero-size fallback
	w, h = FitMediaBox(0, 0, 280, 260, 136, 96)
	if w <= 0 || h <= 0 {
		t.Fatalf("zero-size fallback failed: got %.1fx%.1f", w, h)
	}
}

func TestFormatAudioDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-2 * time.Second, "0:00"},
		{0, "0:00"},
		{9 * time.Second, "0:09"},
		{65 * time.Second, "1:05"},
		{3661 * time.Second, "1:01:01"},
	}
	for _, tc := range cases {
		if got := FormatAudioDuration(tc.d); got != tc.want {
			t.Errorf("FormatAudioDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func writeSampleGradientPNG(t *testing.T, dir, name string, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8(20 + (x*160)/width)
			g := uint8(90 + (y*120)/height)
			b := uint8(200 - (x*90)/width)
			// Subtle bright center badge so preview visually pops.
			dx := x - width/2
			dy := y - height/2
			if dx*dx+dy*dy < (height/5)*(height/5) {
				r, g, b = 248, 250, 252
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create sample png: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatalf("encode sample png: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close sample png: %v", err)
	}
	return path
}

func TestInlineMediaPreviewLightboxAndUpdateUI(t *testing.T) {
	outDir := os.Getenv("TAILCHAT_SNAPSHOT_DIR")
	tmpDir := t.TempDir()
	samplePNG := writeSampleGradientPNG(t, tmpDir, "architecture-preview.png", 480, 300)
	fakeAudio := filepath.Join(tmpDir, "voice-note.mp3")
	if err := os.WriteFile(fakeAudio, []byte("fake-audio-placeholder"), 0o600); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}
	fakeVideo := filepath.Join(tmpDir, "screen-recording.mp4")
	if err := os.WriteFile(fakeVideo, []byte("fake-video-placeholder"), 0o600); err != nil {
		t.Fatalf("write fake video: %v", err)
	}

	mock, st, app, tt := newTestApp(t)
	t.Cleanup(func() { app.Close() })

	// Wire a local httptest GitHub Releases endpoint returning v1.1.0 (no external network).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.1.0",
			"html_url": "https://github.com/jae-jae/tailchat/releases/tag/v1.1.0",
			"body": "• Inline preview for images, GIFs, audio, and video posters\n• Built-in signed auto-updater with delta patches\n• Image Lightbox viewer with dimensions and quick actions",
			"draft": false,
			"prerelease": false
		}`))
	}))
	t.Cleanup(srv.Close)

	mgr := updater.NewManager(updater.Options{
		Repo:       "jae-jae/tailchat",
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
	})
	st.SetUpdater(mgr)
	st.CheckForUpdates(context.Background(), true)

	// Wait for CheckForUpdates goroutine to publish Available state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st.Snapshot().Update.Available {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !st.Snapshot().Update.Available {
		t.Fatalf("expected update to be available after CheckForUpdates")
	}

	// Replace messages with rich media showcase: local image, audio, video, and remote image.
	now := time.Now().UnixMilli()
	pngInfo, _ := os.Stat(samplePNG)
	mock.mu.Lock()
	mock.messages = []model.Message{
		{
			MsgID:          "msg-img-local",
			AuthorDeviceID: "dev-self-001",
			AuthorName:     "MacBook Pro",
			AuthorSeq:      1,
			Kind:           model.MessageKindFile,
			BlobHash:       "blake3-img-001",
			CreatedAt:      now - 40_000,
			File: &model.FileBlob{
				BlobHash:  "blake3-img-001",
				FileName:  "architecture-preview.png",
				FileSize:  pngInfo.Size(),
				MimeType:  "image/png",
				LocalPath: samplePNG,
				Seeders:   []string{"dev-self-001"},
			},
		},
		{
			MsgID:          "msg-aud-local",
			AuthorDeviceID: "dev-peer-002",
			AuthorName:     "iPhone 16",
			AuthorSeq:      2,
			Kind:           model.MessageKindFile,
			BlobHash:       "blake3-aud-001",
			CreatedAt:      now - 30_000,
			File: &model.FileBlob{
				BlobHash:  "blake3-aud-001",
				FileName:  "voice-note.mp3",
				FileSize:  384 * 1024,
				MimeType:  "audio/mpeg",
				LocalPath: fakeAudio,
				Seeders:   []string{"dev-peer-002"},
			},
		},
		{
			MsgID:          "msg-vid-local",
			AuthorDeviceID: "dev-peer-002",
			AuthorName:     "iPhone 16",
			AuthorSeq:      3,
			Kind:           model.MessageKindFile,
			BlobHash:       "blake3-vid-001",
			CreatedAt:      now - 20_000,
			File: &model.FileBlob{
				BlobHash:  "blake3-vid-001",
				FileName:  "screen-recording.mp4",
				FileSize:  6 * 1024 * 1024,
				MimeType:  "video/mp4",
				LocalPath: fakeVideo,
				Seeders:   []string{"dev-peer-002"},
			},
		},
		{
			MsgID:          "msg-img-remote",
			AuthorDeviceID: "dev-peer-002",
			AuthorName:     "iPhone 16",
			AuthorSeq:      4,
			Kind:           model.MessageKindFile,
			BlobHash:       "blake3-img-remote",
			CreatedAt:      now - 10_000,
			File: &model.FileBlob{
				BlobHash: "blake3-img-remote",
				FileName: "sunset-4k.jpg",
				FileSize: 12 * 1024 * 1024,
				MimeType: "image/jpeg",
				Seeders:  []string{"dev-peer-002"},
			},
		},
	}
	mock.mu.Unlock()
	st.RefreshAll()

	// Trigger initial frame to kick off async image decode, then wait until ready.
	tt.Frame()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entry := app.media.GetImage("blake3-img-001", samplePNG, chatThumbMaxSide)
		if entry != nil && entry.state == mediaReady {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	tt.Frame()

	// Verify update banner + all media items are rendered.
	for _, want := range []string{
		"发现 TailChat v1.1.0 新版本",
		"查看更新",
		"architecture-preview.png",
		"voice-note.mp3",
		"screen-recording.mp4",
		"sunset-4k.jpg",
		"下载并预览",
	} {
		if !tt.HasText(want) {
			t.Fatalf("expected media+update main view to contain %q; texts: %v", want, tt.Texts())
		}
	}

	// Render 2x snapshot of main window with inline media + update banner.
	imgMain := mygoui.Render(app.View, 540, 740, 2)
	assertValid2xRender(t, imgMain, outDir, "05_media_and_update_banner.png")

	// Click inline image preview to open Lightbox modal.
	if err := tt.Click("查看大图"); err != nil {
		t.Fatalf("click inline image preview: %v", err)
	}
	tt.Frame()
	if !app.lightboxOpen {
		t.Fatalf("expected lightbox modal to open after clicking inline image")
	}

	// Wait briefly for lightbox high-res image decode.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entry := app.media.GetImage("blake3-img-001", samplePNG, lightboxMaxSide)
		if entry != nil && entry.state == mediaReady {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	tt.Frame()

	if !tt.HasText("480×300") || !tt.HasText("打开文件") || !tt.HasText("打开所在目录") {
		t.Fatalf("expected lightbox header to show dimensions 480×300, 打开文件, and 打开所在目录; texts: %v", tt.Texts())
	}
	imgLB := mygoui.Render(app.View, 540, 740, 2)
	assertValid2xRender(t, imgLB, outDir, "06_image_lightbox.png")

	// Close Lightbox.
	if err := tt.Click("关闭预览"); err != nil {
		t.Fatalf("click close lightbox: %v", err)
	}
	tt.Frame()
	if app.lightboxOpen {
		t.Fatalf("expected lightbox closed after clicking close button")
	}

	// Open Update Modal from the top banner.
	if err := tt.Click("查看更新"); err != nil {
		t.Fatalf("click 查看更新: %v", err)
	}
	tt.Frame()
	if !app.updateModalOpen {
		t.Fatalf("expected update modal to open")
	}
	for _, want := range []string{"发现新版本", "v1.0.0 → v1.1.0", "前往下载页"} {
		if !tt.HasText(want) {
			t.Fatalf("expected update modal to contain %q; texts: %v", want, tt.Texts())
		}
	}
	imgUp := mygoui.Render(app.View, 540, 740, 2)
	assertValid2xRender(t, imgUp, outDir, "07_update_modal.png")

	// Close Update Modal and dismiss banner.
	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("click 关闭 on update modal: %v", err)
	}
	tt.Frame()
	if err := tt.Click("暂不提醒"); err != nil {
		t.Fatalf("click dismiss update banner: %v", err)
	}
	tt.Frame()
	if tt.HasText("发现 TailChat v1.1.0 新版本") {
		t.Fatalf("expected update banner hidden after dismiss")
	}

	// Open Settings, switch to About tab, and verify Software Updates card exists.
	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	tt.Frame()
	if err := tt.Click("关于"); err != nil {
		t.Fatalf("switch to 关于 tab: %v", err)
	}
	tt.Frame()
	for _, want := range []string{"软件更新", "发现新版本：v1.1.0", "查看更新"} {
		if !tt.HasText(want) {
			t.Fatalf("expected Settings About tab to contain %q; texts: %v", want, tt.Texts())
		}
	}
	imgSet := mygoui.Render(app.View, 540, 740, 2)
	assertValid2xRender(t, imgSet, outDir, "08_settings_with_updates.png")
}
