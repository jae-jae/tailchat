package transport

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"

	"tailchat/internal/model"
)

const (
	defaultTailcatRegionID tailcfg.DERPRegionID = 304
	defaultCustomRegionID  tailcfg.DERPRegionID = 900
	defaultCustomDERPPort                       = 443
	defaultCustomSTUNPort                       = 3478
)

// BuildServerDERPConfig translates a user DERPConfig into tailcat.Server DERP fields.
func BuildServerDERPConfig(cfg model.DERPConfig) (regionID tailcfg.DERPRegionID, region *tailcfg.DERPRegion, mapURL string, err error) {
	mode := cfg.Mode
	if mode == "" {
		mode = model.DERPModeDefault
	}

	switch mode {
	case model.DERPModeDefault:
		rid := tailcfg.DERPRegionID(cfg.RegionID)
		if rid <= 0 {
			rid = defaultTailcatRegionID
		}
		return rid, nil, "", nil

	case model.DERPModeCustomRegion:
		host := strings.TrimSpace(cfg.CustomHost)
		if host == "" {
			return 0, nil, "", errors.New("custom DERP region requires custom_host")
		}
		rid := tailcfg.DERPRegionID(cfg.RegionID)
		if rid <= 0 {
			rid = defaultCustomRegionID
		}
		port := cfg.CustomDERPPort
		if port <= 0 {
			port = defaultCustomDERPPort
		}
		stunPort := cfg.CustomSTUNPort
		if stunPort == 0 {
			stunPort = defaultCustomSTUNPort
		}
		regionName := strings.TrimSpace(cfg.CustomRegionName)
		if regionName == "" {
			regionName = fmt.Sprintf("Custom DERP (%s)", host)
		}
		r := &tailcfg.DERPRegion{
			RegionID:   rid,
			RegionCode: "custom",
			RegionName: regionName,
			Nodes: []*tailcfg.DERPNode{
				{
					Name:     "custom-1",
					RegionID: rid,
					HostName: host,
					DERPPort: port,
					STUNPort: stunPort,
				},
			},
		}
		return rid, r, "", nil

	case model.DERPModeCustomMapURL:
		u := strings.TrimSpace(cfg.DERPMapURL)
		if u == "" {
			return 0, nil, "", errors.New("custom DERP map mode requires derp_map_url")
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return 0, nil, "", fmt.Errorf("invalid derp_map_url %q: must start with http:// or https://", u)
		}
		if cfg.RegionID < 0 {
			return 0, nil, "", fmt.Errorf("invalid region_id %d: must be >= 0", cfg.RegionID)
		}
		return tailcfg.DERPRegionID(cfg.RegionID), nil, u, nil

	default:
		return 0, nil, "", fmt.Errorf("unsupported DERP mode %q", cfg.Mode)
	}
}

// FormatServerAddr returns the tailcat.Addr clients should use for server s under cfg.
// For DERPModeDefault and DERPModeCustomMapURL (when a positive RegionID is known),
// it encodes a compact RegionID-only ConnInfo; for DERPModeCustomRegion it returns
// the inline Server.TailcatAddr() embedding the self-hosted derper details.
func FormatServerAddr(s *tailcat.Server, cfg model.DERPConfig) (tailcat.Addr, error) {
	if s == nil {
		return "", errors.New("nil tailcat server")
	}
	fullAddr := s.TailcatAddr()
	if fullAddr == "" {
		return "", errors.New("server has empty tailcat address")
	}

	mode := cfg.Mode
	if mode == "" {
		mode = model.DERPModeDefault
	}

	switch mode {
	case model.DERPModeCustomRegion:
		return fullAddr, nil

	case model.DERPModeDefault, model.DERPModeCustomMapURL:
		ci, err := tailcat.ParseAddr(fullAddr)
		if err != nil {
			return "", fmt.Errorf("parse server tailcat addr: %w", err)
		}
		rid := tailcfg.DERPRegionID(cfg.RegionID)
		if rid <= 0 {
			if mode == model.DERPModeDefault {
				rid = defaultTailcatRegionID
			} else if s.RegionID > 0 {
				rid = s.RegionID
			}
		}
		if rid > 0 {
			ci.Region = nil
			ci.RegionID = rid
			return ci.Addr(), nil
		}
		// Auto-picked region under custom DERP map without an explicit RegionID:
		// keep the inline region address so clients can connect reliably.
		return fullAddr, nil

	default:
		return "", fmt.Errorf("unsupported DERP mode %q", cfg.Mode)
	}
}

// derpMapURLForPeer returns the DERPMapURL to use when dialing a peer, preferring
// the peer's advertised custom DERP map URL and falling back to the local DERP map URL.
func derpMapURLForPeer(peerDERP, localDERP model.DERPConfig) string {
	if peerDERP.Mode == model.DERPModeCustomMapURL {
		if u := strings.TrimSpace(peerDERP.DERPMapURL); u != "" {
			return u
		}
	}
	if localDERP.Mode == model.DERPModeCustomMapURL {
		if u := strings.TrimSpace(localDERP.DERPMapURL); u != "" {
			return u
		}
	}
	return ""
}

func normalizeDERPConfig(cfg model.DERPConfig) model.DERPConfig {
	if cfg.Mode == "" {
		cfg.Mode = model.DERPModeDefault
	}
	if cfg.Mode == model.DERPModeDefault && cfg.RegionID <= 0 {
		cfg.RegionID = int(defaultTailcatRegionID)
	}
	if cfg.Mode == model.DERPModeCustomRegion {
		if cfg.RegionID <= 0 {
			cfg.RegionID = int(defaultCustomRegionID)
		}
		if cfg.CustomDERPPort <= 0 {
			cfg.CustomDERPPort = defaultCustomDERPPort
		}
		if cfg.CustomSTUNPort == 0 {
			cfg.CustomSTUNPort = defaultCustomSTUNPort
		}
	}
	return cfg
}
