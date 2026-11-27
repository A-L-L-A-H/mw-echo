package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MediaStreamUploadFunc streams media directly to 9Drive object storage without buffering to local disk.
type MediaStreamUploadFunc func(ctx context.Context, project, logicalPath, filename, mimeType string, reader io.Reader) (string, error)

// MediaStreamDownloadFunc opens a direct stream from 9Drive object storage by storage object ID.
type MediaStreamDownloadFunc func(ctx context.Context, objectID string) (filename string, mimeType string, body io.ReadCloser, err error)

// MediaMetadata indexes a 9Drive-offloaded media object in SQLite without storing binary payloads in the DB.
type MediaMetadata struct {
	MediaID         string `json:"media_id"`
	DeviceID        string `json:"device_id"`
	Filename        string `json:"filename"`
	MimeType        string `json:"mime_type"`
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"size"`
	StorageObjectID string `json:"storage_object_id"`
	CreatedAt       string `json:"created_at"`
}

// StreamUploadMedia pipes reader directly into the configured 9Drive stream (or HubStore fallback) and indexes metadata in SQLite.
func (m *Manager) StreamUploadMedia(ctx context.Context, deviceID, logicalPath, filename, mimeType string, reader io.Reader) (MediaMetadata, error) {
	if reader == nil {
		return MediaMetadata{}, errors.New("media reader is required")
	}
	cleanDevice := normalizeDeviceID(deviceID)
	resolvedName := filepath.Base(strings.TrimSpace(filename))
	if resolvedName == "" || resolvedName == "." || resolvedName == "/" {
		resolvedName = "upload.bin"
	}
	resolvedMime := strings.TrimSpace(mimeType)
	if resolvedMime == "" {
		resolvedMime = mime.TypeByExtension(strings.ToLower(filepath.Ext(resolvedName)))
		if resolvedMime == "" {
			resolvedMime = "application/octet-stream"
		}
	}
	if strings.TrimSpace(logicalPath) == "" {
		logicalPath = fmt.Sprintf("%s/uploads/%d_%s", cleanDevice, time.Now().UTC().UnixNano(), resolvedName)
	}

	hasher := sha256.New()
	var totalBytes int64
	countingReader := io.TeeReader(reader, io.MultiWriter(hasher, &byteCounter{n: &totalBytes}))

	var objectID string
	var err error
	switch {
	case m.opts.MediaUploadStream != nil:
		objectID, err = m.opts.MediaUploadStream(ctx, "whatsapp", logicalPath, resolvedName, resolvedMime, countingReader)
	case m.opts.ArchiveMedia != nil:
		raw, readErr := io.ReadAll(io.LimitReader(countingReader, 64*1024*1024))
		if readErr != nil {
			return MediaMetadata{}, readErr
		}
		objectID, err = m.opts.ArchiveMedia(ctx, "whatsapp", logicalPath, resolvedName, resolvedMime, raw)
	default:
		savedObj, putErr := m.hub.PutBlob(ctx, "whatsapp", logicalPath, resolvedName, resolvedMime, countingReader)
		if putErr == nil {
			objectID = savedObj.ID
		}
		err = putErr
	}
	if err != nil {
		return MediaMetadata{}, fmt.Errorf("offload media stream: %w", err)
	}
	if totalBytes == 0 {
		return MediaMetadata{}, errors.New("media payload is empty")
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	mediaID := "med_" + digest[:24]
	record := MediaMetadata{
		MediaID:         mediaID,
		DeviceID:        cleanDevice,
		Filename:        resolvedName,
		MimeType:        resolvedMime,
		SHA256:          digest,
		SizeBytes:       totalBytes,
		StorageObjectID: objectID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := m.upsertMediaMetadataRow(ctx, record); err != nil {
		return MediaMetadata{}, err
	}
	return record, nil
}

// StreamDownloadMedia opens a streaming reader for a media ID from 9Drive, HubStore, or whatsmeow fallback.
func (m *Manager) StreamDownloadMedia(ctx context.Context, deviceID, mediaID string) (MediaMetadata, io.ReadCloser, error) {
	trimmedID := strings.TrimSpace(mediaID)
	if trimmedID == "" {
		return MediaMetadata{}, nil, errors.New("media id is required")
	}

	meta, err := m.getMediaMetadataRow(ctx, trimmedID)
	if err == nil {
		if m.opts.MediaDownloadStream != nil && meta.StorageObjectID != "" {
			name, mimeType, body, dlErr := m.opts.MediaDownloadStream(ctx, meta.StorageObjectID)
			if dlErr == nil && body != nil {
				if name != "" {
					meta.Filename = name
				}
				if mimeType != "" {
					meta.MimeType = mimeType
				}
				return meta, body, nil
			}
		}
		if meta.StorageObjectID != "" {
			if blobObj, diskPath, blobErr := m.hub.GetBlob(ctx, meta.StorageObjectID); blobErr == nil && diskPath != "" {
				fileBody, openErr := os.Open(diskPath)
				if openErr == nil {
					if meta.Filename == "" {
						meta.Filename = blobObj.Filename
					}
					if meta.MimeType == "" {
						meta.MimeType = blobObj.MimeType
					}
					return meta, fileBody, nil
				}
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return MediaMetadata{}, nil, err
	}

	// Fallback: resolve by WhatsApp message ID if caller passed a message ID.
	filename, mimeType, content, msgErr := m.DownloadMessageMedia(ctx, deviceID, trimmedID)
	if msgErr != nil {
		return MediaMetadata{}, nil, msgErr
	}
	return MediaMetadata{
		MediaID:   trimmedID,
		DeviceID:  normalizeDeviceID(deviceID),
		Filename:  filename,
		MimeType:  mimeType,
		SizeBytes: int64(len(content)),
	}, io.NopCloser(bytes.NewReader(content)), nil
}

func (m *Manager) indexMessageMediaMetadata(ctx context.Context, deviceID, msgID, filename, mimeType, objectID string, content []byte) {
	if len(content) == 0 && objectID == "" {
		return
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	_ = m.upsertMediaMetadataRow(ctx, MediaMetadata{
		MediaID:         msgID,
		DeviceID:        normalizeDeviceID(deviceID),
		Filename:        filename,
		MimeType:        mimeType,
		SHA256:          digest,
		SizeBytes:       int64(len(content)),
		StorageObjectID: objectID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	})
}

type byteCounter struct {
	n *int64
}

func (b *byteCounter) Write(p []byte) (int, error) {
	*b.n += int64(len(p))
	return len(p), nil
}
