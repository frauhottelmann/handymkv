//go:build windows

package hmkv

import (
	"fmt"
	"strings"
	"syscall"
)

// IOCTL_STORAGE_EJECT_MEDIA: CTL_CODE(IOCTL_STORAGE_BASE, 0x0202, METHOD_BUFFERED, FILE_READ_ACCESS)
const ioctlStorageEjectMedia = 0x2D4808

// ejectWindowsDrive opens the drive (e.g. "E:") and sends IOCTL_STORAGE_EJECT_MEDIA.
func ejectWindowsDrive(devicePath string) error {
	m := windowsDriveLetter.FindStringSubmatch(devicePath)
	if m == nil {
		return fmt.Errorf("could not determine the drive letter from %q", devicePath)
	}

	path, err := syscall.UTF16PtrFromString(`\\.\` + strings.ToUpper(m[1]) + ":")
	if err != nil {
		return err
	}

	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("could not open the drive: %w", err)
	}
	defer syscall.CloseHandle(handle)

	var returned uint32
	if err := syscall.DeviceIoControl(handle, ioctlStorageEjectMedia, nil, 0, nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("eject failed: %w", err)
	}

	return nil
}
