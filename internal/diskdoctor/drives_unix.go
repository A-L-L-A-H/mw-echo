//go:build !windows

package diskdoctor

import (
	"fmt"
	"os"
	"syscall"
)

const twoGiB = 2 * 1024 * 1024 * 1024

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

// ListDrives enumerates distinct Unix mountpoints using syscall.Statfs.
func ListDrives() ([]DriveInfo, error) {
	candidates := []string{"/", "/home", "/var", "/tmp", "/data", "/mnt", "/Volumes"}
	seenFS := make(map[string]struct{}, len(candidates))
	drives := make([]DriveInfo, 0, len(candidates))

	for _, mountPath := range candidates {
		if _, err := os.Stat(mountPath); err != nil {
			continue
		}
		var fsStat syscall.Statfs_t
		if err := syscall.Statfs(mountPath, &fsStat); err != nil {
			continue
		}
		fsKey := fmt.Sprintf("%v-%d-%d", fsStat.Fsid, fsStat.Blocks, fsStat.Bsize)
		if _, exists := seenFS[fsKey]; exists {
			continue
		}
		seenFS[fsKey] = struct{}{}

		blockSize := uint64(fsStat.Bsize)
		totalBytes := fsStat.Blocks * blockSize
		if totalBytes == 0 {
			continue
		}
		freeBytes := fsStat.Bavail * blockSize
		usedBytes := uint64(0)
		if totalBytes > freeBytes {
			usedBytes = totalBytes - freeBytes
		}
		usedPct := (float64(usedBytes) / float64(totalBytes)) * 100.0

		drives = append(drives, DriveInfo{
			Drive:      mountPath,
			Label:      mountPath,
			TotalBytes: totalBytes,
			UsedBytes:  usedBytes,
			FreeBytes:  freeBytes,
			UsedPct:    usedPct,
			Status:     classifyDriveStatus(usedPct, freeBytes),
		})
	}

	return drives, nil
}
