package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tonis/c3pkg/internal/config"
	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/internal/store"
	"github.com/tonis/c3pkg/migrations"
)

const usage = `usage:
  c3pkg [serve]                     run the web server (default)
  c3pkg admin grant <email>         make a registered user an administrator
  c3pkg admin revoke <email>        remove administrator rights
  c3pkg backup [dir]                write a consistent SQLite snapshot (keeps the newest 14)
  c3pkg healthcheck                 exit 0 if the local server answers /healthz
`

func runAdmin(cfg config.Config, args []string) error {
	if len(args) != 2 || (args[0] != "grant" && args[0] != "revoke") {
		return fmt.Errorf("%s", usage)
	}
	d, err := db.Open(cfg.DBPath(), migrations.FS)
	if err != nil {
		return err
	}
	defer d.Close()
	st := &store.Store{DB: d}
	if err := st.SetAdminByEmail(contextBackground(), args[1], args[0] == "grant"); err != nil {
		if err == store.ErrNotFound {
			return fmt.Errorf("no user with email %s (they must register first)", args[1])
		}
		return err
	}
	fmt.Printf("admin %s: %s\n", args[0], args[1])
	return nil
}

// runBackup uses VACUUM INTO, which is safe while the server is running (WAL mode).
func runBackup(cfg config.Config, args []string) error {
	dir := filepath.Join(cfg.DataDir, "backups")
	if len(args) > 0 {
		dir = args[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	d, err := db.Open(cfg.DBPath(), migrations.FS)
	if err != nil {
		return err
	}
	defer d.Close()
	dest := filepath.Join(dir, "c3pkg-"+time.Now().UTC().Format("20060102-150405")+".db")
	if _, err := d.Exec(`VACUUM INTO ?`, dest); err != nil {
		return err
	}
	fmt.Println("wrote", dest)

	old, _ := filepath.Glob(filepath.Join(dir, "c3pkg-*.db"))
	sort.Strings(old)
	for len(old) > 14 {
		os.Remove(old[0])
		old = old[1:]
	}
	return nil
}

// runHealthcheck is used by the container HEALTHCHECK (the image has no shell or curl).
func runHealthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", strings.TrimSpace(resp.Status))
	}
	return nil
}
