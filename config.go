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

// renderCommand is the exact external command that was last used to render the
// wallpaper (typically feh plus its args). Only the most recent one is kept, so
// --restore can re-run precisely what was applied last, across all fit and
// mirror modes.
type renderCommand struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// monitorState is the wallpaper currently shown on a single monitor: the source
// image and the fit mode it is rendered with. Because feh can only apply one
// mode per invocation, the app keeps its own per-monitor record so that changing
// one monitor can rebuild the full desktop composite without disturbing the
// image or mode of the others. The map is keyed by monitor name (from xrandr).
type monitorState struct {
	Image string `json:"image"`
	Mode  string `json:"mode"`
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

// lastCommandPath returns the path to the file holding the single most recent
// render command, creating its parent directory if needed.
func lastCommandPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "simplewall")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "last-command.json"), nil
}

// monitorStatePath returns the path to the file holding the per-monitor
// wallpaper state, creating its parent directory if needed.
func monitorStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "simplewall")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "monitors.json"), nil
}

// loadMonitorState reads the persisted per-monitor state. A missing or
// unreadable file yields an empty map, so callers can seed it from scratch.
func loadMonitorState() map[string]monitorState {
	state := map[string]monitorState{}
	path, err := monitorStatePath()
	if err != nil {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("monitor-state: %v", err)
		return map[string]monitorState{}
	}
	return state
}

// saveMonitorState overwrites the stored per-monitor state. Failures are logged
// but not fatal: losing the record only means a later single-monitor change
// falls back to reading ~/.fehbg to reconstruct the others.
func saveMonitorState(state map[string]monitorState) {
	path, err := monitorStatePath()
	if err != nil {
		log.Printf("monitor-state: %v", err)
		return
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("monitor-state: %v", err)
		return
	}

	// Write to a temp file and rename so a crash mid-write can't leave a
	// truncated, unparseable state file behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("monitor-state: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("monitor-state: %v", err)
	}
}

// saveLastCommand overwrites the stored render command with cmd, keeping only
// the latest. Failures are logged but not fatal: losing the ability to restore
// is not worth crashing over.
func saveLastCommand(cmd renderCommand) {
	path, err := lastCommandPath()
	if err != nil {
		log.Printf("last-command: %v", err)
		return
	}

	data, err := json.MarshalIndent(cmd, "", "  ")
	if err != nil {
		log.Printf("last-command: %v", err)
		return
	}

	// Write to a temp file and rename so a crash mid-write can't leave a
	// truncated, unparseable command behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("last-command: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("last-command: %v", err)
	}
}

// loadLastCommand reads the stored render command. A missing or unreadable file
// yields an error, since there is nothing to restore.
func loadLastCommand() (renderCommand, error) {
	var cmd renderCommand
	path, err := lastCommandPath()
	if err != nil {
		return cmd, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cmd, err
	}
	if err := json.Unmarshal(data, &cmd); err != nil {
		return cmd, err
	}
	return cmd, nil
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
