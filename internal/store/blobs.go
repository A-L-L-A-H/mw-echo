package store

import (
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

func (s *HubStore) blobFilePath(digest string) (string, error) {
	if len(digest) != 64 {
		return "", errors.New("invalid blob digest")
	}
	for _, ch := range digest {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return "", errors.New("non-hex blob digest")
		}
	}
	return filepath.Join(s.blobDir, digest[:2], digest), nil
}

func (s *HubStore) PutBlob(ctx context.Context, project, logicalPath, filename, mimeType string,
	reader io.Reader) (StorageObject, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "default"
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == "/" {
		return StorageObject{}, errors.New("valid filename is required")
	}
	logicalPath = filepath.ToSlash(strings.TrimLeft(strings.TrimSpace(logicalPath), "/\\"))
	if logicalPath == "" {
		logicalPath = filename
	}
	if strings.Contains(logicalPath, "..") {
		return StorageObject{}, errors.New("logicalPath cannot contain traversal")
	}

	tmpFile, err := os.CreateTemp(s.blobDir, "upload-*")
	if err != nil {
		return StorageObject{}, fmt.Errorf("create temp blob: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		_ = tmpFile.Close()
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), reader)
	if err != nil {
		return StorageObject{}, fmt.Errorf("write blob: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return StorageObject{}, err
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	destPath, err := s.blobFilePath(digest)
	if err != nil {
		return StorageObject{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return StorageObject{}, err
	}
	if _, statErr := os.Stat(destPath); errors.Is(statErr, os.ErrNotExist) {
		if err := os.Rename(tmpPath, destPath); err != nil {
			return StorageObject{}, fmt.Errorf("commit blob: %w", err)
		}
		cleanupTmp = false
	}

	if strings.TrimSpace(mimeType) == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
	}

	obj := StorageObject{
		ID:          makeDeterministicID("blob", project, logicalPath),
		Project:     project,
		LogicalPath: logicalPath,
		Filename:    filename,
		MimeType:    mimeType,
		SHA256:      digest,
		SizeBytes:   written,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO storage_objects (id, project, logical_path, filename, mime_type, sha256, size_bytes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project, logical_path) DO UPDATE SET
  filename = excluded.filename,
  mime_type = excluded.mime_type,
  sha256 = excluded.sha256,
  size_bytes = excluded.size_bytes,
  created_at = excluded.created_at
`, obj.ID, obj.Project, obj.LogicalPath, obj.Filename, obj.MimeType, obj.SHA256, obj.SizeBytes, obj.CreatedAt)
	if err != nil {
		return StorageObject{}, err
	}
	return obj, nil
}

func (s *HubStore) RecordRemoteBlob(ctx context.Context, project, logicalPath, filename, mimeType, digest string,
	sizeBytes int64) (StorageObject, error) {
	obj := StorageObject{
		ID:          makeDeterministicID("blob", project, logicalPath),
		Project:     project,
		LogicalPath: logicalPath,
		Filename:    filename,
		MimeType:    mimeType,
		SHA256:      digest,
		SizeBytes:   sizeBytes,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO storage_objects (id, project, logical_path, filename, mime_type, sha256, size_bytes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project, logical_path) DO UPDATE SET
  filename = excluded.filename,
  mime_type = excluded.mime_type,
  sha256 = excluded.sha256,
  size_bytes = excluded.size_bytes,
  created_at = excluded.created_at
`, obj.ID, obj.Project, obj.LogicalPath, obj.Filename, obj.MimeType, obj.SHA256, obj.SizeBytes, obj.CreatedAt)
	if err != nil {
		return StorageObject{}, err
	}
	return obj, nil
}

func (s *HubStore) ListBlobs(ctx context.Context, project, searchQuery string, limit int) ([]StorageObject, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var clauses []string
	var args []any
	if strings.TrimSpace(project) != "" {
		clauses = append(clauses, "project = ?")
		args = append(args, strings.TrimSpace(project))
	}
	if strings.TrimSpace(searchQuery) != "" {
		pattern := "%" + strings.ToLower(strings.TrimSpace(searchQuery)) + "%"
		clauses = append(clauses, "(LOWER(logical_path) LIKE ? OR LOWER(filename) LIKE ? OR LOWER(mime_type) LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, project, logical_path, filename, mime_type, sha256, size_bytes, created_at
FROM storage_objects %s ORDER BY created_at DESC, logical_path ASC LIMIT ?`, whereSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	objects := make([]StorageObject, 0)
	for rows.Next() {
		var obj StorageObject
		if err := rows.Scan(&obj.ID, &obj.Project, &obj.LogicalPath, &obj.Filename, &obj.MimeType, &obj.SHA256,
			&obj.SizeBytes, &obj.CreatedAt); err != nil {
			return nil, err
		}
		objects = append(objects, obj)
	}
	return objects, rows.Err()
}

func (s *HubStore) GetBlob(ctx context.Context, id string) (StorageObject, string, error) {
	var obj StorageObject
	err := s.db.QueryRowContext(ctx, `
SELECT id, project, logical_path, filename, mime_type, sha256, size_bytes, created_at
FROM storage_objects WHERE id = ?
`, id).Scan(&obj.ID, &obj.Project, &obj.LogicalPath, &obj.Filename, &obj.MimeType, &obj.SHA256, &obj.SizeBytes, &obj.CreatedAt)
	if err != nil {
		return StorageObject{}, "", err
	}
	diskPath, err := s.blobFilePath(obj.SHA256)
	if err != nil {
		return StorageObject{}, "", err
	}
	return obj, diskPath, nil
}

func (s *HubStore) DeleteBlob(ctx context.Context, id string) error {
	obj, diskPath, err := s.GetBlob(ctx, id)
	if err != nil {
		return err
	}
	execRes, err := s.db.ExecContext(ctx, `DELETE FROM storage_objects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := execRes.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	var remainingRefs int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_objects WHERE sha256 = ?`, obj.SHA256).Scan(&remainingRefs)
	if remainingRefs == 0 {
		_ = os.Remove(diskPath)
	}
	return nil
}
