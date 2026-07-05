package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
)

// config is the persisted state written under ~/.config/simplewall. It is
// deliberately tiny: the app has no need for a database, so a single JSON file
// is enough to remember the folders the user last added between sessions.
type config struct {
	Folders []string `json:"folders"`
}

// configPath returns the path to the config file, creating its parent
// directory if needed.
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "simplewall")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadConfig reads the persisted config. A missing or unreadable file yields an
// empty config rather than an error, so a first run just starts blank.
func loadConfig() config {
	var cfg config
	path, err := configPath()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("config: %v", err)
		return config{}
	}
	return cfg
}

// saveConfig snapshots the current folders and writes them to disk. It is safe
// to call from any goroutine; failures are logged but not fatal, since losing
// the persisted list is not worth crashing over.
func (a *wallpaperApp) saveConfig() {
	a.mu.Lock()
	cfg := config{Folders: slices.Clone(a.folders)}
	a.mu.Unlock()

	path, err := configPath()
	if err != nil {
		log.Printf("config: %v", err)
		return
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Printf("config: %v", err)
		return
	}

	// Write to a temp file and rename so a crash mid-write can't truncate the
	// existing config.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("config: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("config: %v", err)
	}
}

// restoreFolders re-adds the folders persisted from a previous session. It runs
// addFolder for each so their images are rescanned and thumbnailed exactly as
// if the user had just picked them.
func (a *wallpaperApp) restoreFolders() {
	for _, dir := range loadConfig().Folders {
		a.addFolder(dir)
	}
}
