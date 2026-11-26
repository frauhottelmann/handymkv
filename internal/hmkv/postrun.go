package hmkv

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShutdownDelay is the time between the end of the run and the shutdown, so that the shutdown
// can still be cancelled.
const ShutdownDelay = 60 * time.Second

// RunOverrides holds the command line flags that override settings of the configuration for a
// single run. A nil value means the flag was not given and the configuration applies.
type RunOverrides struct {
	// -auto: selects the titles automatically (auto_select_titles).
	AutoSelect *bool
	// -eject: ejects the discs after a successful run (eject_after_run).
	Eject *bool
	// -shutdown: shuts down the computer after a successful run (shutdown_after_run).
	Shutdown *bool
}

// resolveBool returns the override if it is set, the configured value otherwise.
func resolveBool(configured bool, override *bool) bool {
	if override != nil {
		return *override
	}
	return configured
}

// runSucceeded reports whether ripping and encoding succeeded, i.e. the post-run actions
// (eject, shutdown) may run: at least one title was processed and no title failed.
func runSucceeded(processed int, ripFailures int, encodedCount int) bool {
	return processed > 0 && ripFailures == 0 && encodedCount == processed
}

// discDevicePaths returns the distinct drive paths of the given titles, in order of appearance.
func discDevicePaths(titles []TitleInfo) []string {
	seen := make(map[string]struct{})
	var paths []string

	for _, t := range titles {
		p := strings.TrimSpace(t.DevicePath)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	return paths
}

// runCommand runs an external command. It is a variable so that tests can replace it.
var runCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ejectWindows ejects a drive on Windows. It is a variable so that tests can replace it.
var ejectWindows = ejectWindowsDrive

// sleep waits before the shutdown on macOS. It is a variable so that tests can replace it.
var sleep = time.Sleep

// ejectCommand returns the command that ejects the disc in the given drive on Linux and macOS.
// devicePath is the drive path reported by makemkvcon, e.g. "/dev/sr0" or "/dev/rdisk4".
func ejectCommand(goos, devicePath string) ([]string, error) {
	switch goos {
	case "linux":
		return []string{"eject", devicePath}, nil
	case "darwin":
		// makemkvcon reports the raw device (/dev/rdiskN), diskutil expects the disk (/dev/diskN).
		return []string{"diskutil", "eject", strings.Replace(devicePath, "/dev/rdisk", "/dev/disk", 1)}, nil
	default:
		return nil, fmt.Errorf("ejecting the disc is not supported on %s", goos)
	}
}

// shutdownCommand returns the command that shuts down the computer after ShutdownDelay and the
// command that cancels it. On macOS the delay is waited for by handymkv itself (cancel with
// Ctrl+C), since "shutdown" needs root there; cancel is empty then.
func shutdownCommand(goos string) (cmd []string, cancel string, err error) {
	switch goos {
	case "windows":
		return []string{"shutdown", "/s", "/t", fmt.Sprintf("%d", int(ShutdownDelay.Seconds()))}, "shutdown /a", nil
	case "linux":
		return []string{"shutdown", "-h", fmt.Sprintf("+%d", int(ShutdownDelay.Minutes()))}, "shutdown -c", nil
	case "darwin":
		return []string{"osascript", "-e", `tell application "System Events" to shut down`}, "", nil
	default:
		return nil, "", fmt.Errorf("shutting down is not supported on %s", goos)
	}
}

// ejectDiscs ejects the discs in the given drives. Errors are printed as warnings.
func ejectDiscs(goos string, devicePaths []string) {
	if len(devicePaths) == 0 {
		fmt.Printf("Warning: the disc could not be ejected because makemkvcon did not report the drive.\n")
		return
	}

	for _, p := range devicePaths {
		fmt.Printf("Ejecting disc in %s...\n", p)

		var err error
		if goos == "windows" {
			err = ejectWindows(p)
		} else {
			var cmd []string
			cmd, err = ejectCommand(goos, p)
			if err == nil {
				err = runCommand(cmd[0], cmd[1:]...)
			}
		}

		if err != nil {
			fmt.Printf("Warning: could not eject the disc in %s: %v\n", p, err)
		}
	}
}

// shutdownComputer shuts down the computer after ShutdownDelay. Errors are printed as warnings.
func shutdownComputer(goos string) {
	cmd, cancel, err := shutdownCommand(goos)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
		return
	}

	if cancel != "" {
		fmt.Printf("The computer will shut down in %d seconds. To cancel, run: %s\n\n", int(ShutdownDelay.Seconds()), cancel)
	} else {
		fmt.Printf("The computer will shut down in %d seconds. Press Ctrl+C to cancel.\n\n", int(ShutdownDelay.Seconds()))
		sleep(ShutdownDelay)
	}

	if err := runCommand(cmd[0], cmd[1:]...); err != nil {
		fmt.Printf("Warning: could not shut down the computer: %v\n\n", err)
	}
}

// runPostRunActions ejects the discs and/or shuts down the computer, as configured.
func runPostRunActions(eject, shutdown bool, titles []TitleInfo) {
	if !eject && !shutdown {
		return
	}

	if eject {
		ejectDiscs(runtime.GOOS, discDevicePaths(titles))
		fmt.Println()
	}

	if shutdown {
		shutdownComputer(runtime.GOOS)
	}
}
