package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"tailchat/internal/store"
	"tailchat/internal/syncproto"
	"tailchat/internal/transport"
	"tailchat/internal/ui"
	"tailchat/plugins/mobilebridge"
)

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

	bridge := mobilebridge.Attach(ctx, svc)
	defer bridge.Close()

	if err := ui.Run(svc, nil); err != nil {
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
