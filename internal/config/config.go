package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr          string
	BaseURL       string
	DataDir       string
	SessionSecret string
	MaxUploadMB   int64
	MaxUnzippedMB int64
	MaxFiles      int

	// TrustedProxies are the networks whose X-Forwarded-For header is believed.
	TrustedProxies []*net.IPNet

	GitHubClientID     string
	GitHubClientSecret string
}

func (c Config) DBPath() string      { return filepath.Join(c.DataDir, "c3pkg.db") }
func (c Config) UploadsDir() string  { return filepath.Join(c.DataDir, "uploads") }
func (c Config) SecureCookies() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func Load() (Config, error) {
	c := Config{
		Addr:               env("C3PKG_ADDR", ":8080"),
		BaseURL:            strings.TrimRight(env("C3PKG_BASE_URL", "http://localhost:8080"), "/"),
		DataDir:            env("C3PKG_DATA_DIR", "./data"),
		SessionSecret:      os.Getenv("C3PKG_SESSION_SECRET"),
		MaxUploadMB:        envInt("C3PKG_MAX_UPLOAD_MB", 20),
		MaxUnzippedMB:      envInt("C3PKG_MAX_UNZIPPED_MB", 200),
		MaxFiles:           int(envInt("C3PKG_MAX_FILES", 5000)),
		GitHubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
	}
	nets, err := parseCIDRs(env("C3PKG_TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"))
	if err != nil {
		return c, err
	}
	c.TrustedProxies = nets
	if c.SecureCookies() && len(c.SessionSecret) < 32 {
		return c, fmt.Errorf("C3PKG_SESSION_SECRET must be at least 32 characters when C3PKG_BASE_URL is https")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil && v > 0 {
		return v
	}
	return def
}

func parseCIDRs(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("C3PKG_TRUSTED_PROXIES: %w", err)
		}
		out = append(out, n)
	}
	return out, nil
}
