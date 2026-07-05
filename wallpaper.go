package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// errNoDesktopArea is returned when the composed desktop would have no area,
// meaning monitor geometry could not be determined.
var errNoDesktopArea = errors.New("could not determine desktop area")

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

// monitorGeom is a single monitor's position and size in the X screen, as
// reported by xrandr --listmonitors. Offsets let composeDesktop place each
// monitor's rendered region at the right spot in the stitched desktop image.
type monitorGeom struct {
	name       string
	w, h, x, y int
}

// monitorLineRe extracts WIDTH, HEIGHT, X, Y from an xrandr --listmonitors
// geometry token such as "1920/476x1080/268+1920+0" (the /NNN parts are the
// physical millimetre sizes, which we ignore).
var monitorLineRe = regexp.MustCompile(`^(\d+)/\d+x(\d+)/\d+([+-]\d+)([+-]\d+)$`)

// getMonitorGeoms returns the connected monitors with their geometry, in xrandr
// order. A nil result means geometry is unavailable (xrandr missing or failed).
func getMonitorGeoms() []monitorGeom {
	out, err := exec.Command("xrandr", "--listmonitors").Output()
	if err != nil {
		return nil
	}

	var geoms []monitorGeom
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ":") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSuffix(fields[0], ":")); err != nil {
			continue
		}
		m := monitorLineRe.FindStringSubmatch(fields[2])
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		x, _ := strconv.Atoi(m[3])
		y, _ := strconv.Atoi(m[4])
		geoms = append(geoms, monitorGeom{name: fields[len(fields)-1], w: w, h: h, x: x, y: y})
	}
	return geoms
}

// getMonitors returns the screen choices for the UI: "All" followed by each
// monitor name in xrandr order. It falls back to just "All" when geometry is
// unavailable.
func getMonitors() []string {
	geoms := getMonitorGeoms()
	result := []string{"All"}
	for _, g := range geoms {
		result = append(result, g.name)
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

// setWallpaper applies imagePath with the given fit mode to the chosen screen.
//
// Each monitor is tracked independently (image + mode) so that different
// monitors can show different images rendered with different modes at the same
// time — something feh cannot express, since a single feh call applies one
// --bg-* mode to every screen. To work around that, the app renders its own
// full-desktop composite (one region per monitor, each with its own mode) and
// hands the single stitched image to feh.
//
// When screen is "All" every monitor is set to imagePath/mode. Otherwise only
// the named monitor changes and the others keep their stored image and mode.
func setWallpaper(imagePath, mode, screen string) error {
	geoms := getMonitorGeoms()

	// Fallback for setups where monitor geometry can't be read: keep the old
	// single-image behaviour so the app still works headless of xrandr.
	if len(geoms) == 0 {
		if isMirrorMode(mode) {
			return fmt.Errorf("mirror mode requires exactly two monitors")
		}
		flag, ok := fehFlags[mode]
		if !ok {
			flag = "--bg-fill"
		}
		return runFeh([]string{flag, imagePath})
	}

	if isMirrorMode(mode) {
		return setMirrorWallpaper(imagePath, mode, geoms)
	}

	state := seedMonitorState(geoms)
	if screen == "All" || len(geoms) == 1 {
		for _, g := range geoms {
			state[g.name] = monitorState{Image: imagePath, Mode: mode}
		}
	} else {
		state[screen] = monitorState{Image: imagePath, Mode: mode}
	}

	return composeAndApply(geoms, state)
}

// setMirrorWallpaper shows imagePath on one monitor and a horizontally mirrored
// copy on the other, spanning both screens. mirror-α puts the normal image on
// the first monitor and the mirror on the second; mirror-β swaps them. It runs
// through the same composite pipeline as the fit modes, so a later single-screen
// change leaves the mirrored setup on the untouched monitor intact.
func setMirrorWallpaper(imagePath, mode string, geoms []monitorGeom) error {
	if len(geoms) != 2 {
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

	state := seedMonitorState(geoms)
	state[geoms[0].name] = monitorState{Image: first, Mode: "fill"}
	state[geoms[1].name] = monitorState{Image: second, Mode: "fill"}

	return composeAndApply(geoms, state)
}

// seedMonitorState loads the saved per-monitor state and fills in any monitor
// that has no record yet from the current ~/.fehbg (defaulting to fill mode).
// This preserves whatever each monitor was already showing the first time a
// single-monitor change is made, before the app has stored its own state.
func seedMonitorState(geoms []monitorGeom) map[string]monitorState {
	state := loadMonitorState()

	var missing bool
	for _, g := range geoms {
		if _, ok := state[g.name]; !ok {
			missing = true
			break
		}
	}
	if !missing {
		return state
	}

	imgs := readFehbg(len(geoms))
	for i, g := range geoms {
		if _, ok := state[g.name]; ok {
			continue
		}
		st := monitorState{Mode: "fill"}
		if i < len(imgs) {
			st.Image = imgs[i]
		}
		state[g.name] = st
	}
	return state
}

// composeAndApply renders the full-desktop composite from state, applies it with
// feh, and persists the state only after feh succeeds so a failed render can't
// leave the stored state out of sync with what is on screen.
func composeAndApply(geoms []monitorGeom, state map[string]monitorState) error {
	out, err := composeDesktop(geoms, state)
	if err != nil {
		return err
	}

	// --no-xinerama makes feh treat the whole root as one screen and --bg-tile
	// lays the composite down from the top-left; since the composite is exactly
	// the desktop size, it lands once and covers everything. feh still writes
	// ~/.fehbg, so login restore (e.g. exec ~/.fehbg) keeps working.
	if err := runFeh([]string{"--no-xinerama", "--bg-tile", out}); err != nil {
		return err
	}

	saveMonitorState(state)
	return nil
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
