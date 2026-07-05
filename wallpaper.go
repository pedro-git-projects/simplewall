package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var fehModes = []string{"fill", "scale", "max", "center", "tile"}

var fehFlags = map[string]string{
	"fill":   "--bg-fill",
	"scale":  "--bg-scale",
	"max":    "--bg-max",
	"center": "--bg-center",
	"tile":   "--bg-tile",
}

// Mirror modes span two monitors: one shows the wallpaper normally, the other
// shows a horizontally-flipped copy. The α/β variants differ only in which
// monitor gets which. They are only offered when exactly two monitors exist.
const (
	modeMirrorA = "mirror-α"
	modeMirrorB = "mirror-β"
)

var mirrorModes = []string{modeMirrorA, modeMirrorB}

func isMirrorMode(mode string) bool {
	return mode == modeMirrorA || mode == modeMirrorB
}

// runFeh runs feh with the given args and, on success, persists the exact
// command as the single latest render command so --restore can reproduce it.
func runFeh(args []string) error {
	if err := exec.Command("feh", args...).Run(); err != nil {
		return err
	}
	saveLastCommand(renderCommand{Name: "feh", Args: args})
	return nil
}

// restoreLast re-runs the most recent render command previously applied by the
// program. It is used by the --restore CLI flag to reapply the last wallpaper
// configuration without opening the GUI.
func restoreLast() error {
	cmd, err := loadLastCommand()
	if err != nil {
		return fmt.Errorf("no previous wallpaper to restore: %w", err)
	}
	if cmd.Name == "" {
		return fmt.Errorf("no previous wallpaper to restore")
	}
	return exec.Command(cmd.Name, cmd.Args...).Run()
}

func getMonitors() []string {
	out, err := exec.Command("xrandr", "--listmonitors").Output()
	if err != nil {
		return []string{"All"}
	}

	result := []string{"All"}

	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ":") {
			continue
		}

		index := strings.TrimSuffix(fields[0], ":")
		if _, err := strconv.Atoi(index); err != nil {
			continue
		}

		result = append(result, fields[len(fields)-1])
	}

	return result
}

// readFehbg parses ~/.fehbg and returns the current per-monitor image paths.
func readFehbg(n int) []string {
	result := make([]string, n)

	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".fehbg"))
	if err != nil {
		return result
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "feh ") {
			continue
		}

		rest := line
		i := 0
		last := ""

		for {
			start := strings.IndexByte(rest, '\'')
			if start < 0 {
				break
			}

			rest = rest[start+1:]

			end := strings.IndexByte(rest, '\'')
			if end < 0 {
				break
			}

			path := rest[:end]
			last = path

			if i < len(result) {
				result[i] = path
			}

			i++
			rest = rest[end+1:]
		}

		for ; i < len(result) && last != ""; i++ {
			result[i] = last
		}

		return result
	}

	return result
}

func setWallpaper(imagePath, mode, screen string, monitors []string) error {
	if isMirrorMode(mode) {
		return setMirrorWallpaper(imagePath, mode, monitors)
	}

	flag, ok := fehFlags[mode]
	if !ok {
		flag = "--bg-fill"
	}

	// Let feh persist the full command to ~/.fehbg so readFehbg can recover the
	// real current per-monitor state on the next call. Using --no-fehbg here
	// would leave that file stale, causing a later single-screen change to
	// re-apply an outdated wallpaper to the other monitors and clobber them.
	args := []string{flag}

	numMonitors := len(monitors) - 1 // exclude "All"
	if screen == "All" || numMonitors <= 1 {
		args = append(args, imagePath)
	} else {
		monIdx := -1
		for i, m := range monitors[1:] {
			if m == screen {
				monIdx = i
				break
			}
		}
		if monIdx < 0 {
			args = append(args, imagePath)
		} else {
			current := readFehbg(numMonitors)
			current[monIdx] = imagePath
			args = append(args, current...)
		}
	}

	return runFeh(args)
}

// setMirrorWallpaper applies imagePath to one monitor and a horizontally
// mirrored copy to the other, spanning both screens. mirror-α puts the normal
// image on the first monitor and the mirror on the second; mirror-β swaps them.
// Monitor order follows getMonitors (i.e. feh's own detection order).
func setMirrorWallpaper(imagePath, mode string, monitors []string) error {
	numMonitors := len(monitors) - 1 // exclude "All"
	if numMonitors != 2 {
		return fmt.Errorf("mirror mode requires exactly two monitors")
	}

	mirrored, err := mirrorImage(imagePath)
	if err != nil {
		return err
	}

	first, second := imagePath, mirrored
	if mode == modeMirrorB {
		first, second = mirrored, imagePath
	}

	return runFeh([]string{"--bg-fill", first, second})
}

func pickFolder() (string, error) {
	if out, err := exec.Command("zenity", "--file-selection", "--directory", "--title=Select Wallpaper Folder").Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	home, _ := os.UserHomeDir()
	if out, err := exec.Command("kdialog", "--getexistingdirectory", home).Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("install zenity or kdialog for folder selection")
}
