package hmkv

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestResolveBool(t *testing.T) {
	yes, no := true, false

	if resolveBool(false, nil) || !resolveBool(true, nil) {
		t.Fatal("without override the configured value must apply")
	}
	if !resolveBool(false, &yes) || resolveBool(true, &no) {
		t.Fatal("the override must take precedence over the configuration")
	}
}

func TestRunSucceeded(t *testing.T) {
	cases := []struct {
		name                         string
		processed, failures, encoded int
		want                         bool
	}{
		{"all ripped and encoded", 3, 0, 3, true},
		{"rip failure", 3, 1, 2, false},
		{"not all encoded", 3, 0, 2, false},
		{"nothing processed", 0, 0, 0, false},
	}

	for _, c := range cases {
		if got := runSucceeded(c.processed, c.failures, c.encoded); got != c.want {
			t.Errorf("%s: runSucceeded = %t, want %t", c.name, got, c.want)
		}
	}
}

func TestDiscDevicePaths(t *testing.T) {
	titles := []TitleInfo{
		{DevicePath: "/dev/sr0"},
		{DevicePath: "/dev/sr0"},
		{DevicePath: ""},
		{DevicePath: "/dev/sr1"},
	}

	got := discDevicePaths(titles)
	if !slices.Equal(got, []string{"/dev/sr0", "/dev/sr1"}) {
		t.Fatalf("discDevicePaths = %v", got)
	}
}

func TestEjectCommand(t *testing.T) {
	cmd, err := ejectCommand("linux", "/dev/sr0")
	if err != nil || !slices.Equal(cmd, []string{"eject", "/dev/sr0"}) {
		t.Fatalf("linux: %v, %v", cmd, err)
	}

	cmd, err = ejectCommand("darwin", "/dev/rdisk4")
	if err != nil || !slices.Equal(cmd, []string{"diskutil", "eject", "/dev/disk4"}) {
		t.Fatalf("darwin: %v, %v", cmd, err)
	}

	if _, err := ejectCommand("plan9", "x"); err == nil {
		t.Fatal("expected an error for an unsupported system")
	}
}

func TestShutdownCommand(t *testing.T) {
	t.Setenv("SystemRoot", `C:\WINDOWS`)
	cmd, cancel, err := shutdownCommand("windows")
	if err != nil || !slices.Equal(cmd, []string{`C:\WINDOWS\System32\shutdown.exe`, "/s", "/t", "60"}) || cancel != "shutdown /a" {
		t.Fatalf("windows: %v, %q, %v", cmd, cancel, err)
	}

	cmd, cancel, err = shutdownCommand("linux")
	if err != nil || !slices.Equal(cmd, []string{"shutdown", "-h", "+1"}) || cancel != "shutdown -c" {
		t.Fatalf("linux: %v, %q, %v", cmd, cancel, err)
	}

	cmd, cancel, err = shutdownCommand("darwin")
	if err != nil || cmd[0] != "osascript" || cancel != "" {
		t.Fatalf("darwin: %v, %q, %v", cmd, cancel, err)
	}

	if _, _, err := shutdownCommand("plan9"); err == nil {
		t.Fatal("expected an error for an unsupported system")
	}
}

// stubPostRun replaces the external commands and returns the recorded calls.
func stubPostRun(t *testing.T) *[]string {
	t.Helper()

	var calls []string
	origRun, origEject, origSleep := runCommand, ejectWindows, sleep

	runCommand = func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	ejectWindows = func(devicePath string) error {
		calls = append(calls, "ioctl-eject "+devicePath)
		return nil
	}
	sleep = func(d time.Duration) {
		calls = append(calls, "sleep "+d.String())
	}

	t.Cleanup(func() { runCommand, ejectWindows, sleep = origRun, origEject, origSleep })

	return &calls
}

func TestEjectDiscs(t *testing.T) {
	calls := stubPostRun(t)

	ejectDiscs("linux", []string{"/dev/sr0", "/dev/sr1"})
	ejectDiscs("windows", []string{"E:"})

	want := []string{"eject /dev/sr0", "eject /dev/sr1", "ioctl-eject E:"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestShutdownComputer(t *testing.T) {
	calls := stubPostRun(t)
	t.Setenv("SystemRoot", `C:\Windows`)

	shutdownComputer("windows")
	shutdownComputer("darwin")

	want := []string{
		`C:\Windows\System32\shutdown.exe /s /t 60`,
		"sleep 1m0s",
		`osascript -e tell application "System Events" to shut down`,
	}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestRunPostRunActionsDisabled(t *testing.T) {
	calls := stubPostRun(t)

	runPostRunActions(false, false, []TitleInfo{{DevicePath: "/dev/sr0"}})

	if len(*calls) != 0 {
		t.Fatalf("expected no commands, got %v", *calls)
	}
}

func TestPostRunConfigKeys(t *testing.T) {
	var config handyMKVConfig
	if err := json.Unmarshal([]byte(`{"eject_after_run": true, "shutdown_after_run": true}`), &config); err != nil {
		t.Fatal(err)
	}
	if !config.EjectAfterRun || !config.ShutdownAfterRun {
		t.Fatalf("config keys not read: %+v", config)
	}

	out, err := json.Marshal(handyMKVConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "eject_after_run") || strings.Contains(string(out), "shutdown_after_run") {
		t.Fatalf("disabled options should be omitted: %s", out)
	}
}

func TestWindowsSystem32Tool(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Win\`)
	if got := windowsSystem32Tool("shutdown.exe"); got != `D:\Win\System32\shutdown.exe` {
		t.Fatalf("got %q", got)
	}

	t.Setenv("SystemRoot", "")
	if got := windowsSystem32Tool("shutdown.exe"); got != `C:\Windows\System32\shutdown.exe` {
		t.Fatalf("got %q", got)
	}
}
