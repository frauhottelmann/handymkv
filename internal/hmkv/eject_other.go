//go:build !windows

package hmkv

import "fmt"

// ejectWindowsDrive is only available on Windows.
func ejectWindowsDrive(devicePath string) error {
	return fmt.Errorf("ejecting a Windows drive is not supported on this system")
}
