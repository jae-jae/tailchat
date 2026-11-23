package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
	_ "github.com/egoist/mygo/audio/flac"
	_ "github.com/egoist/mygo/audio/mp3"
	_ "github.com/egoist/mygo/audio/mp4"
	_ "github.com/egoist/mygo/audio/vorbis"
	mygoui "github.com/egoist/mygo/ui"
)

// MediaKind classifies a file message for inline preview rendering.
type MediaKind uint8

const (
	MediaOther MediaKind = iota
	MediaImage
	MediaAnimated
	MediaAudio
	MediaVideo
)

const (
	maxCachedMediaEntries = 48
	maxImageReadBytes     = 50 << 20 // 50 MiB safety cap
	maxAnimReadBytes      = 25 << 20 // 25 MiB safety cap
	chatThumbMaxSide      = 640
	lightboxMaxSide       = 1600
)

// ClassifyMedia determines the inline preview kind from filename and optional MIME type.
func ClassifyMedia(fileName, mimeType string) MediaKind {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
	mime := strings.ToLower(strings.TrimSpace(mimeType))

	switch ext {
	case ".gif", ".apng":
		return MediaAnimated
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".svg":
		return MediaImage
	case ".mp3", ".wav", ".ogg", ".oga", ".flac", ".m4a", ".aac":
		return MediaAudio
	case ".mp4", ".mov", ".m4v", ".mkv", ".webm", ".avi":
		return MediaVideo
	}

	switch {
	case mime == "image/gif" || mime == "image/apng":
		return MediaAnimated
	case strings.HasPrefix(mime, "image/"):
		return MediaImage
	case strings.HasPrefix(mime, "audio/"):
		return MediaAudio
	case strings.HasPrefix(mime, "video/"):
		return MediaVideo
	default:
		return MediaOther
	}
}

// FitMediaBox computes an aspect-ratio-preserving display size within maxW x maxH.
func FitMediaBox(srcW, srcH, maxW, maxH, minW, minH float32) (float32, float32) {
	if srcW <= 0 || srcH <= 0 {
		return maxW, maxH * 0.65
	}
	scale := maxW / srcW
	if srcH*scale > maxH {
		scale = maxH / srcH
	}
	// Avoid upscaling tiny icons/stickers excessively beyond 2x.
	if scale > 2 {
		scale = 2
	}
	w := srcW * scale
	h := srcH * scale
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if w > maxW {
		w = maxW
	}
	if h > maxH {
		h = maxH
	}
	return w, h
}

type mediaLoadState uint8

const (
	mediaLoading mediaLoadState = iota
	mediaReady
	mediaFailed
)

type mediaImageEntry struct {
	state   mediaLoadState
	src     mygoui.ImageSource
	width   float32
	height  float32
	origW   int
	origH   int
	lastUse int64
}

type mediaAnimEntry struct {
	state    mediaLoadState
	decoder  mygoui.VideoDecoder
	player   *mygoui.VideoPlayer
	fallback mygoui.ImageSource
	width    float32
	height   float32
	lastUse  int64
}

// MediaCache asynchronously decodes and caches image thumbnails, lightbox bitmaps,
// animated GIF/APNG decoders, and video poster frames without blocking the UI thread.
type MediaCache struct {
	mu         sync.Mutex
	images     map[string]*mediaImageEntry
	anims      map[string]*mediaAnimEntry
	seq        int64
	invalidate func()
	decodeSem  chan struct{}
	posterSem  chan struct{}
	thumbDir   string
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewMediaCache creates a bounded async media preview cache.
func NewMediaCache(invalidate func()) *MediaCache {
	thumbDir := filepath.Join(os.TempDir(), "tailchat-media-thumbs")
	_ = os.MkdirAll(thumbDir, 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	return &MediaCache{
		images:     make(map[string]*mediaImageEntry),
		anims:      make(map[string]*mediaAnimEntry),
		invalidate: invalidate,
		decodeSem:  make(chan struct{}, 4),
		posterSem:  make(chan struct{}, 2),
		thumbDir:   thumbDir,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetInvalidate updates the UI redraw callback.
func (mc *MediaCache) SetInvalidate(fn func()) {
	mc.mu.Lock()
	mc.invalidate = fn
	mc.mu.Unlock()
}

func (mc *MediaCache) triggerInvalidate() {
	mc.mu.Lock()
	fn := mc.invalidate
	mc.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Close cancels pending background work and releases active animated video players.
func (mc *MediaCache) Close() {
	if mc.cancel != nil {
		mc.cancel()
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	for k, e := range mc.anims {
		if e.player != nil {
			e.player.Close()
			e.player = nil
		}
		delete(mc.anims, k)
	}
	clear(mc.images)
}

// GetImage returns a cached static image entry or kicks off async decoding.
func (mc *MediaCache) GetImage(cacheKey, localPath string, maxSide int) *mediaImageEntry {
	if localPath == "" {
		return nil
	}
	fullKey := fmt.Sprintf("img:%d:%s:%s", maxSide, cacheKey, localPath)

	mc.mu.Lock()
	if e, ok := mc.images[fullKey]; ok {
		mc.seq++
		e.lastUse = mc.seq
		cp := *e
		mc.mu.Unlock()
		return &cp
	}
	mc.seq++
	entry := &mediaImageEntry{state: mediaLoading, lastUse: mc.seq}
	mc.images[fullKey] = entry
	mc.evictImagesLocked()
	mc.mu.Unlock()

	go mc.decodeImageAsync(fullKey, localPath, maxSide)
	return &mediaImageEntry{state: mediaLoading}
}

func (mc *MediaCache) decodeImageAsync(fullKey, localPath string, maxSide int) {
	mc.decodeSem <- struct{}{}
	defer func() { <-mc.decodeSem }()

	src, origW, origH, err := loadScaledImage(localPath, maxSide)

	mc.mu.Lock()
	entry, ok := mc.images[fullKey]
	if ok {
		if err != nil || src == nil {
			entry.state = mediaFailed
		} else {
			w, h := mygoui.ImageSize(src)
			entry.src = src
			entry.width = w
			entry.height = h
			entry.origW = origW
			entry.origH = origH
			entry.state = mediaReady
		}
	}
	mc.mu.Unlock()
	mc.triggerInvalidate()
}

// GetAnimated returns a cached GIF/APNG entry (creating the VideoPlayer lazily on the UI thread).
func (mc *MediaCache) GetAnimated(cacheKey, localPath string) *mediaAnimEntry {
	if localPath == "" {
		return nil
	}
	fullKey := fmt.Sprintf("anim:%s:%s", cacheKey, localPath)

	mc.mu.Lock()
	if e, ok := mc.anims[fullKey]; ok {
		mc.seq++
		e.lastUse = mc.seq
		if e.state == mediaReady && e.player == nil && e.decoder != nil {
			if p, err := mygoui.NewVideoPlayer(e.decoder); err == nil && p != nil {
				p.SetLoop(true)
				e.player = p
				e.decoder = nil
			}
		}
		cp := *e
		mc.mu.Unlock()
		return &cp
	}
	mc.seq++
	entry := &mediaAnimEntry{state: mediaLoading, lastUse: mc.seq}
	mc.anims[fullKey] = entry
	mc.evictAnimsLocked()
	mc.mu.Unlock()

	go mc.decodeAnimAsync(fullKey, localPath)
	return &mediaAnimEntry{state: mediaLoading}
}

func (mc *MediaCache) decodeAnimAsync(fullKey, localPath string) {
	mc.decodeSem <- struct{}{}
	defer func() { <-mc.decodeSem }()

	info, err := os.Stat(localPath)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxAnimReadBytes {
		mc.markAnimFailed(fullKey)
		return
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		mc.markAnimFailed(fullKey)
		return
	}

	ext := strings.ToLower(filepath.Ext(localPath))
	var dec mygoui.VideoDecoder
	if ext == ".apng" {
		dec, err = mygoui.NewAPNGDecoder(data)
	} else {
		dec, err = mygoui.NewGIFDecoder(data)
	}
	fallback, fbErr := mygoui.DecodeBitmapScaled(data, chatThumbMaxSide)
	if err != nil && fbErr != nil {
		mc.markAnimFailed(fullKey)
		return
	}

	var w, h float32
	if fallback != nil {
		w, h = mygoui.ImageSize(fallback)
	}
	if w <= 0 || h <= 0 {
		dw, dh := probeImageDimensions(localPath)
		w, h = float32(dw), float32(dh)
	}

	mc.mu.Lock()
	if entry, ok := mc.anims[fullKey]; ok {
		entry.decoder = dec
		entry.fallback = fallback
		entry.width = w
		entry.height = h
		entry.state = mediaReady
	}
	mc.mu.Unlock()
	mc.triggerInvalidate()
}

func (mc *MediaCache) markAnimFailed(fullKey string) {
	mc.mu.Lock()
	if entry, ok := mc.anims[fullKey]; ok {
		entry.state = mediaFailed
	}
	mc.mu.Unlock()
	mc.triggerInvalidate()
}

// GetVideoPoster returns a cached video first-frame thumbnail or extracts it asynchronously.
func (mc *MediaCache) GetVideoPoster(cacheKey, localPath string) *mediaImageEntry {
	if localPath == "" {
		return nil
	}
	fullKey := fmt.Sprintf("vid:%s:%s", cacheKey, localPath)

	mc.mu.Lock()
	if e, ok := mc.images[fullKey]; ok {
		mc.seq++
		e.lastUse = mc.seq
		cp := *e
		mc.mu.Unlock()
		return &cp
	}
	mc.seq++
	entry := &mediaImageEntry{state: mediaLoading, lastUse: mc.seq}
	mc.images[fullKey] = entry
	mc.evictImagesLocked()
	mc.mu.Unlock()

	go mc.extractVideoPosterAsync(fullKey, localPath)
	return &mediaImageEntry{state: mediaLoading}
}

func (mc *MediaCache) extractVideoPosterAsync(fullKey, localPath string) {
	mc.posterSem <- struct{}{}
	defer func() { <-mc.posterSem }()

	sum := sha256.Sum256([]byte(fullKey))
	posterPath := filepath.Join(mc.thumbDir, hex.EncodeToString(sum[:12])+".png")

	if _, err := os.Stat(posterPath); err != nil {
		if err := extractVideoFramePNG(mc.ctx, localPath, posterPath); err != nil {
			mc.mu.Lock()
			if entry, ok := mc.images[fullKey]; ok {
				entry.state = mediaFailed
			}
			mc.mu.Unlock()
			mc.triggerInvalidate()
			return
		}
	}

	src, origW, origH, err := loadScaledImage(posterPath, chatThumbMaxSide)
	mc.mu.Lock()
	if entry, ok := mc.images[fullKey]; ok {
		if err != nil || src == nil {
			entry.state = mediaFailed
		} else {
			w, h := mygoui.ImageSize(src)
			entry.src = src
			entry.width = w
			entry.height = h
			entry.origW = origW
			entry.origH = origH
			entry.state = mediaReady
		}
	}
	mc.mu.Unlock()
	mc.triggerInvalidate()
}

func (mc *MediaCache) evictImagesLocked() {
	for len(mc.images) > maxCachedMediaEntries {
		var oldestKey string
		var oldestSeq int64 = 1<<63 - 1
		for k, e := range mc.images {
			if e.lastUse < oldestSeq {
				oldestSeq = e.lastUse
				oldestKey = k
			}
		}
		if oldestKey == "" {
			break
		}
		delete(mc.images, oldestKey)
	}
}

func (mc *MediaCache) evictAnimsLocked() {
	const maxAnims = 12
	for len(mc.anims) > maxAnims {
		var oldestKey string
		var oldestSeq int64 = 1<<63 - 1
		for k, e := range mc.anims {
			if e.lastUse < oldestSeq {
				oldestSeq = e.lastUse
				oldestKey = k
			}
		}
		if oldestKey == "" {
			break
		}
		if e := mc.anims[oldestKey]; e != nil && e.player != nil {
			e.player.Close()
		}
		delete(mc.anims, oldestKey)
	}
}

func loadScaledImage(localPath string, maxSide int) (mygoui.ImageSource, int, int, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, 0, 0, err
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > maxImageReadBytes {
		return nil, 0, 0, fmt.Errorf("unsupported image size: %d", info.Size())
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return nil, 0, 0, err
	}

	ext := strings.ToLower(filepath.Ext(localPath))
	if ext == ".svg" {
		src, err := mygoui.DecodeImage(data)
		if err != nil {
			return nil, 0, 0, err
		}
		w, h := mygoui.ImageSize(src)
		return src, int(w), int(h), nil
	}

	origW, origH := probeImageDimensions(localPath)
	bmp, err := mygoui.DecodeBitmapScaled(data, maxSide)
	if err != nil {
		return nil, 0, 0, err
	}
	if origW <= 0 || origH <= 0 {
		w, h := mygoui.ImageSize(bmp)
		origW, origH = int(w), int(h)
	}
	return bmp, origW, origH, nil
}

func probeImageDimensions(localPath string) (int, int) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func extractVideoFramePNG(parentCtx context.Context, videoPath, outPNG string) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 6*time.Second)
	defer cancel()

	// 1. Prefer ffmpeg when installed.
	if ffmpegPath, err := exec.LookPath("ffmpeg"); err == nil {
		cmd := exec.CommandContext(ctx, ffmpegPath,
			"-y", "-loglevel", "error",
			"-ss", "00:00:00.5",
			"-i", videoPath,
			"-frames:v", "1",
			"-vf", "scale=640:-1",
			outPNG,
		)
		if err := cmd.Run(); err == nil {
			if st, statErr := os.Stat(outPNG); statErr == nil && st.Size() > 0 {
				return nil
			}
		}
		// Fallback to frame 0 for very short clips (< 0.5s).
		cmd0 := exec.CommandContext(ctx, ffmpegPath,
			"-y", "-loglevel", "error",
			"-i", videoPath,
			"-frames:v", "1",
			"-vf", "scale=640:-1",
			outPNG,
		)
		if err := cmd0.Run(); err == nil {
			if st, statErr := os.Stat(outPNG); statErr == nil && st.Size() > 0 {
				return nil
			}
		}
	}

	// 2. macOS built-in QuickLook thumbnail generator (zero external dependencies).
	if runtime.GOOS == "darwin" {
		if qlPath, err := exec.LookPath("qlmanage"); err == nil {
			tmpDir, err := os.MkdirTemp("", "tailchat-ql-*")
			if err == nil {
				defer os.RemoveAll(tmpDir)
				cmd := exec.CommandContext(ctx, qlPath, "-t", "-s", "640", "-o", tmpDir, videoPath)
				if err := cmd.Run(); err == nil {
					generated := filepath.Join(tmpDir, filepath.Base(videoPath)+".png")
					if raw, readErr := os.ReadFile(generated); readErr == nil && len(raw) > 0 {
						return os.WriteFile(outPNG, raw, 0o644)
					}
				}
			}
		}
	}

	return fmt.Errorf("no video thumbnail extractor available")
}

// AudioSnapshot describes playback progress for a specific audio message.
type AudioSnapshot struct {
	MsgID    string
	Playing  bool
	Position time.Duration
	Duration time.Duration
}

// AudioController manages single-track pure-Go audio playback for inline audio messages.
type AudioController struct {
	toggleMu   sync.Mutex
	mu         sync.Mutex
	activeID   string
	activePath string
	player     *audio.Player
	tickCancel context.CancelFunc
	invalidate func()
}

// NewAudioController creates an inline audio playback controller.
func NewAudioController(invalidate func()) *AudioController {
	return &AudioController{invalidate: invalidate}
}

// SetInvalidate updates the UI redraw callback.
func (ac *AudioController) SetInvalidate(fn func()) {
	ac.mu.Lock()
	ac.invalidate = fn
	ac.mu.Unlock()
}

func (ac *AudioController) triggerInvalidate() {
	ac.mu.Lock()
	fn := ac.invalidate
	ac.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Snapshot returns current playback status for msgID.
func (ac *AudioController) Snapshot(msgID string) AudioSnapshot {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.activeID != msgID || ac.player == nil {
		return AudioSnapshot{MsgID: msgID}
	}
	return AudioSnapshot{
		MsgID:    msgID,
		Playing:  ac.player.Playing(),
		Position: ac.player.Position(),
		Duration: ac.player.Duration(),
	}
}

// Toggle starts, pauses, or resumes playback for the given audio file message.
func (ac *AudioController) Toggle(msgID, localPath string) error {
	ac.toggleMu.Lock()
	defer ac.toggleMu.Unlock()

	ac.mu.Lock()
	if ac.activeID == msgID && ac.player != nil {
		if ac.player.Playing() {
			ac.player.Pause()
			ac.stopTickerLocked()
			ac.mu.Unlock()
			ac.triggerInvalidate()
			return nil
		}
		if dur := ac.player.Duration(); dur > 0 && ac.player.Position() >= dur-100*time.Millisecond {
			ac.player.Seek(0)
		}
		ac.player.Play()
		ac.startTickerLocked()
		ac.mu.Unlock()
		ac.triggerInvalidate()
		return nil
	}

	ac.closeActiveLocked()
	ac.mu.Unlock()

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	p, err := audio.OpenStream(f)
	if err != nil {
		_ = f.Close()
		return err
	}

	p.OnEnd(func() {
		ac.mu.Lock()
		if ac.player == p {
			ac.stopTickerLocked()
		}
		ac.mu.Unlock()
		ac.triggerInvalidate()
	})

	ac.mu.Lock()
	ac.activeID = msgID
	ac.activePath = localPath
	ac.player = p
	p.Play()
	ac.startTickerLocked()
	ac.mu.Unlock()
	ac.triggerInvalidate()
	return nil
}

// Stop stops any active audio playback and releases resources.
func (ac *AudioController) Stop() {
	ac.mu.Lock()
	ac.closeActiveLocked()
	ac.mu.Unlock()
}

func (ac *AudioController) closeActiveLocked() {
	ac.stopTickerLocked()
	if ac.player != nil {
		ac.player.Close()
		ac.player = nil
	}
	ac.activeID = ""
	ac.activePath = ""
}

func (ac *AudioController) startTickerLocked() {
	ac.stopTickerLocked()
	ctx, cancel := context.WithCancel(context.Background())
	ac.tickCancel = cancel
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ac.triggerInvalidate()
			}
		}
	}()
}

func (ac *AudioController) stopTickerLocked() {
	if ac.tickCancel != nil {
		ac.tickCancel()
		ac.tickCancel = nil
	}
}

// FormatAudioDuration formats a duration as m:ss (or h:mm:ss when >= 1 hour).
func FormatAudioDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Round(time.Second).Seconds())
	if secs >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, (secs%3600)/60, secs%60)
	}
	return fmt.Sprintf("%d:%02d", secs/60, secs%60)
}
