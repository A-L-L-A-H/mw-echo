//go:build windows

package diskdoctor

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	driveTypeFixed  = 3
	driveTypeRemote = 4
	twoGiB          = 2 * 1024 * 1024 * 1024
)

// DriveInfo describes storage capacity and health for a mounted volume.
type DriveInfo struct {
	Drive      string  `json:"drive"`
	Label      string  `json:"label"`
	TotalBytes uint64  `json:"totalBytes"`
	UsedBytes  uint64  `json:"usedBytes"`
	FreeBytes  uint64  `json:"freeBytes"`
	UsedPct    float64 `json:"usedPct"`
	Status     string  `json:"status"`
}

func classifyDriveStatus(usedPct float64, freeBytes uint64) string {
	if usedPct >= 90.0 || freeBytes < twoGiB {
		return "critical"
	}
	if usedPct >= 80.0 {
		return "warn"
	}
	return "ok"
}

// ListDrives enumerates fixed and remote Windows drives using kernel32.dll without CGO.
func ListDrives() ([]DriveInfo, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procGetLogicalDrives := kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW := kernel32.NewProc("GetDriveTypeW")
	procGetDiskFreeSpaceExW := kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInformationW := kernel32.NewProc("GetVolumeInformationW")

	mask, _, callErr := procGetLogicalDrives.Call()
	if mask == 0 {
		return nil, fmt.Errorf("GetLogicalDrives failed: %w", callErr)
	}

	drives := make([]DriveInfo, 0, 8)
	for bit := 0; bit < 26; bit++ {
		if mask&(1<<uint(bit)) == 0 {
			continue
		}
		driveRoot := fmt.Sprintf("%c:\\", 'A'+bit)
		rootPtr, err := syscall.UTF16PtrFromString(driveRoot)
		if err != nil {
			continue
		}

		driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(rootPtr)))
		if driveType != driveTypeFixed && driveType != driveTypeRemote {
			continue
		}

		var freeBytesAvailable, totalBytes, totalFreeBytes uint64
		ok, _, _ := procGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&freeBytesAvailable)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFreeBytes)),
		)
		if ok == 0 || totalBytes == 0 {
			continue
		}

		var volumeNameBuf [261]uint16
		label := ""
		volOk, _, _ := procGetVolumeInformationW.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&volumeNameBuf[0])),
			uintptr(len(volumeNameBuf)),
			0,
			0,
			0,
			0,
			0,
		)
		if volOk != 0 {
			label = syscall.UTF16ToString(volumeNameBuf[:])
		}

		usedBytes := uint64(0)
		if totalBytes > totalFreeBytes {
			usedBytes = totalBytes - totalFreeBytes
		}
		usedPct := (float64(usedBytes) / float64(totalBytes)) * 100.0

		drives = append(drives, DriveInfo{
			Drive:      driveRoot,
			Label:      label,
			TotalBytes: totalBytes,
			UsedBytes:  usedBytes,
			FreeBytes:  totalFreeBytes,
			UsedPct:    usedPct,
			Status:     classifyDriveStatus(usedPct, totalFreeBytes),
		})
	}

	return drives, nil
}
