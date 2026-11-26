package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"tailchat/internal/store"
	"tailchat/internal/syncproto"
	"tailchat/internal/transport"
	"tailchat/internal/ui"
	"tailchat/plugins/mobilebridge"
)

const maxLogFileBytes = 5 << 20 // 5 MiB rotation threshold

// resilientLogWriter writes to the primary persistent log file first and
// mirrors output to an optional secondary stream (such as os.Stderr) without
// failing if the secondary handle is unavailable (e.g. Windows -H windowsgui
// processes launched from Explorer with no attached console).
type resilientLogWriter struct {
	primary io.Writer
	mirror  io.Writer
}

func (w resilientLogWriter) Write(p []byte) (int, error) {
	n, err := w.primary.Write(p)
	if w.mirror != nil {
		_, _ = w.mirror.Write(p)
	}
	return n, err
}

func setupPersistentLog(dataDir string) func() {
	logPath := filepath.Join(dataDir, "tailchat.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() >= maxLogFileBytes {
		_ = os.Rename(logPath, logPath+".1")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("tailchat: failed to open log file %q: %v", logPath, err)
		return func() {}
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(resilientLogWriter{primary: f, mirror: os.Stderr})
	log.Printf("tailchat: started (pid=%d, data_dir=%s)", os.Getpid(), dataDir)
	return func() {
		log.Printf("tailchat: shutting down (pid=%d)", os.Getpid())
		_ = f.Close()
	}
}

func main() {
	defaultDataDir := defaultAppDataDir()
	defaultHost, _ := os.Hostname()
	if defaultHost == "" {
		defaultHost = "TailChat-" + runtimePlatform()
	}

	dataDir := flag.String("data-dir", defaultDataDir, "Directory for SQLite database, CAS chunks, and downloaded files")
	deviceName := flag.String("name", defaultHost, "Default display name for this device")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("tailchat: create data dir %q: %v", *dataDir, err)
	}
	closeLog := setupPersistentLog(*dataDir)
	defer closeLog()

	dbPath := filepath.Join(*dataDir, "tailchat.db")
	casDir := filepath.Join(*dataDir, "cas")
	downloadDir := defaultDownloadDir(*dataDir)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		log.Fatalf("tailchat: create downloads dir %q: %v", downloadDir, err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("tailchat: open store: %v", err)
	}

	cas, err := store.NewCAS(casDir)
	if err != nil {
		_ = st.Close()
		log.Fatalf("tailchat: init CAS: %v", err)
	}

	mesh := transport.NewTailcatMesh(transport.TailcatOptions{})

	svc, err := syncproto.NewService(st, cas, mesh, syncproto.Options{
		Platform:          runtimePlatform(),
		DefaultDeviceName: *deviceName,
		DownloadDir:       downloadDir,
		IdentityGenerator: transport.GenerateLocalIdentity,
	})
	if err != nil {
		_ = st.Close()
		log.Fatalf("tailchat: init sync service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Start(ctx); err != nil {
		_ = st.Close()
		log.Fatalf("tailchat: start sync service: %v", err)
	}

	var (
		bridgeMu sync.Mutex
		bridge   *mobilebridge.Controller
	)
	defer func() {
		bridgeMu.Lock()
		b := bridge
		bridgeMu.Unlock()
		if b != nil {
			b.Close()
		}
	}()

	// Attach mobilebridge inside WhenReady (onReady) because mygo.NativePlugin.Call
	// requires App.Run / App.WhenReady to have started on iOS & Android.
	if err := ui.Run(svc, func() {
		b := mobilebridge.Attach(ctx, svc)
		bridgeMu.Lock()
		bridge = b
		bridgeMu.Unlock()
	}); err != nil {
		log.Fatalf("tailchat: ui exited with error: %v", err)
	}
}

func defaultAppDataDir() string {
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, "tailchat")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".tailchat")
	}
	return ".tailchat-data"
}

func defaultDownloadDir(fallbackDataDir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dir := filepath.Join(home, "Downloads")
		if runtime.GOOS == "ios" {
			dir = filepath.Join(home, "Documents", "Downloads")
		}
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
	}
	return filepath.Join(fallbackDataDir, "downloads")
}

func runtimePlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}
