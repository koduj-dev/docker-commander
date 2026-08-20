//go:build windows

package docker

import "golang.org/x/sys/windows"

// diskFree reports the total and free bytes on the volume containing path.
func diskFree(path string) (totalBytes, freeBytes uint64, err error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeAvail, &total, &totalFree); err != nil {
		return 0, 0, err
	}
	return total, freeAvail, nil
}
