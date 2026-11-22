package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tailchat/internal/model"
	"tailchat/internal/store"
	"tailchat/internal/syncproto"
	"tailchat/internal/transport"
)

func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".tailchat-headless")
	}
	return "./tailchat-headless-data"
}

func defaultDeviceName() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h) + "-headless"
	}
	return "tailchat-headless"
}

func main() {
	var (
		dataDir        = flag.String("data-dir", defaultDataDir(), "directory for SQLite database, CAS chunks, and downloads")
		deviceName     = flag.String("name", "", "display name for this headless node (defaults to hostname)")
		joinCode       = flag.String("join", "", "pairing code (tailchat-pair://...) to join an existing device group")
		pairMode       = flag.Bool("pair", false, "generate and print a one-time pairing code on startup")
		pairTTL        = flag.Int("pair-ttl", 300, "pairing code TTL in seconds when -pair is enabled")
		derpHost       = flag.String("derp-host", "", "custom self-hosted DERP hostname (switches DERP mode to custom_region)")
		derpMapURL     = flag.String("derp-map-url", "", "custom DERPMap JSON URL (switches DERP mode to custom_map_url)")
		autoDownloadMB = flag.Int64("auto-download-mb", 100, "auto-download file threshold in MiB (0 disables auto-download)")
	)
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data-dir %q: %v", *dataDir, err)
	}

	dbPath := filepath.Join(*dataDir, "tailchat.db")
	casDir := filepath.Join(*dataDir, "cas")
	dlDir := filepath.Join(*dataDir, "downloads")

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open sqlite store %q: %v", dbPath, err)
	}
	cas, err := store.NewCAS(casDir)
	if err != nil {
		_ = st.Close()
		log.Fatalf("open CAS store %q: %v", casDir, err)
	}
	tr := transport.NewTailcatMesh(transport.TailcatOptions{
		Logf: log.Printf,
	})

	name := strings.TrimSpace(*deviceName)
	if name == "" {
		name = defaultDeviceName()
	}

	svc, err := syncproto.NewService(st, cas, tr, syncproto.Options{
		Platform:          "headless",
		DefaultDeviceName: name,
		DownloadDir:       dlDir,
		IdentityGenerator: transport.GenerateLocalIdentity,
	})
	if err != nil {
		_ = st.Close()
		log.Fatalf("create service: %v", err)
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := svc.Start(startCtx); err != nil {
		startCancel()
		_ = st.Close()
		log.Fatalf("start service: %v", err)
	}
	startCancel()

	// Apply CLI runtime config overrides.
	cfg := svc.Config()
	if strings.TrimSpace(*deviceName) != "" {
		cfg.DeviceName = strings.TrimSpace(*deviceName)
	}
	if *autoDownloadMB >= 0 {
		cfg.AutoDownloadMaxBytes = *autoDownloadMB * 1024 * 1024
	}
	cfg.DownloadDir = dlDir

	if err := svc.UpdateConfig(context.Background(), cfg); err != nil {
		log.Printf("warning: update config: %v", err)
	}

	// Apply custom DERP flag if provided.
	if host := strings.TrimSpace(*derpHost); host != "" {
		derpCfg := model.DERPConfig{
			Mode:             model.DERPModeCustomRegion,
			RegionID:         900,
			CustomHost:       host,
			CustomDERPPort:   443,
			CustomSTUNPort:   3478,
			CustomRegionName: "Custom-" + host,
		}
		derpCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := svc.UpdateDERP(derpCtx, derpCfg); err != nil {
			log.Printf("warning: update DERP host %q: %v", host, err)
		} else {
			log.Printf("switched DERP to custom region host %q", host)
		}
		cancel()
	} else if mapURL := strings.TrimSpace(*derpMapURL); mapURL != "" {
		derpCfg := model.DERPConfig{
			Mode:       model.DERPModeCustomMapURL,
			DERPMapURL: mapURL,
		}
		derpCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := svc.UpdateDERP(derpCtx, derpCfg); err != nil {
			log.Printf("warning: update DERP map URL %q: %v", mapURL, err)
		} else {
			log.Printf("switched DERP to custom map URL %q", mapURL)
		}
		cancel()
	}

	id := svc.Identity()
	log.Printf("tailchat-headless started: name=%q device_id=%s group_id=%s auto_download_mb=%d",
		svc.Config().DeviceName, id.DeviceID, id.GroupID, svc.Config().AutoDownloadMaxBytes/(1024*1024))

	if code := strings.TrimSpace(*joinCode); code != "" {
		log.Printf("joining group with provided pairing code...")
		joinCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := svc.JoinWithPairingCode(joinCtx, code); err != nil {
			cancel()
			shutdownGracefully(svc)
			log.Fatalf("join with pairing code failed: %v", err)
		}
		cancel()
		log.Printf("joined group %s successfully", svc.Identity().GroupID)
	}

	if *pairMode {
		pairCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		code, tok, err := svc.CreatePairingCode(pairCtx, *pairTTL)
		cancel()
		if err != nil {
			shutdownGracefully(svc)
			log.Fatalf("create pairing code failed: %v", err)
		}
		expiresAt := time.UnixMilli(tok.ExpiresAt).Format(time.RFC3339)
		fmt.Printf("\n=== Tailchat Pairing Code (expires %s) ===\n%s\n====================================================\n\n", expiresAt, code)
	}

	evCh, unsub := svc.Subscribe()
	defer unsub()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	log.Printf("daemon running (Ctrl+C or SIGTERM to exit)")

	for {
		select {
		case sig := <-sigCh:
			log.Printf("received signal %s, shutting down gracefully...", sig)
			shutdownGracefully(svc)
			log.Printf("shutdown complete")
			return

		case ev, ok := <-evCh:
			if !ok {
				return
			}
			logAppEvent(ev)
		}
	}
}

func shutdownGracefully(svc *syncproto.Service) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		log.Printf("warning: graceful close returned error: %v", err)
	}
}

func logAppEvent(ev model.AppEvent) {
	switch ev.Type {
	case model.EventMessageAdded:
		if ev.Message != nil {
			if ev.Message.Kind == model.MessageKindFile && ev.Message.File != nil {
				log.Printf("[msg] %s sent file %q (%d bytes, hash=%s)",
					ev.Message.AuthorName, ev.Message.File.FileName, ev.Message.File.FileSize, ev.Message.BlobHash)
			} else {
				log.Printf("[msg] %s: %s", ev.Message.AuthorName, ev.Message.TextBody)
			}
		}
	case model.EventMessageDeleted:
		if ev.Message != nil {
			log.Printf("[msg] %s deleted message %s", ev.Message.AuthorName, ev.Message.TargetMsgID)
		}
	case model.EventTransferUpdated:
		if ev.Transfer != nil && (ev.Transfer.State == model.TransferDone || ev.Transfer.State == model.TransferFailed) {
			log.Printf("[transfer] blob=%s state=%s (%d/%d bytes) err=%q",
				ev.Transfer.BlobHash, ev.Transfer.State, ev.Transfer.BytesDone, ev.Transfer.BytesTotal, ev.Transfer.LastError)
		}
	case model.EventPeerStatus:
		if ev.Peer != nil {
			log.Printf("[peer] device=%s connected=%v path=%s", ev.Peer.DeviceID, ev.Peer.Connected, ev.Peer.PathKind)
		}
	case model.EventDevicesUpdated:
		log.Printf("[group] paired device list updated")
	case model.EventError:
		log.Printf("[error] %s", ev.ErrText)
	}
}
