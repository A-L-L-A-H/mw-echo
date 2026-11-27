package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

type ImportSummary struct {
	Project         string `json:"project"`
	SourceDir       string `json:"sourceDir"`
	DocsImported    int    `json:"docsImported"`
	TasksImported   int    `json:"tasksImported"`
	AssetsImported  int    `json:"assetsImported"`
	SkippedTooLarge int    `json:"skippedTooLarge"`
}

var skippedDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	".next":        true,
	"target":       true,
	"vendor":       true,
}

var assetExtensions = map[string]bool{
	".png":   true,
	".jpg":   true,
	".jpeg":  true,
	".webp":  true,
	".avif":  true,
	".svg":   true,
	".pdf":   true,
	".webm":  true,
	".mp4":   true,
	".woff2": true,
}

const maxMarkdownBytes = 2 * 1024 * 1024
const maxAssetBytes = 50 * 1024 * 1024

func ImportDocsDirectory(ctx context.Context, hub *store.HubStore, project, docsRoot string,
	includeAssets bool) (ImportSummary, error) {
	absRoot, err := filepath.Abs(docsRoot)
	if err != nil {
		return ImportSummary{}, err
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return ImportSummary{}, fmt.Errorf("directory not found: %s", absRoot)
	}
	summary := ImportSummary{Project: project, SourceDir: absRoot}

	err = filepath.WalkDir(absRoot, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if skippedDirNames[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(absRoot, currentPath)
		if err != nil {
			return nil
		}
		relPosix := filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		fileInfo, err := entry.Info()
		if err != nil {
			return nil
		}

		if ext == ".md" || ext == ".mdx" || ext == ".txt" {
			if fileInfo.Size() > maxMarkdownBytes {
				summary.SkippedTooLarge++
				return nil
			}
			rawBytes, err := os.ReadFile(currentPath)
			if err != nil {
				return nil
			}
			_, err = hub.UpsertDoc(ctx, store.DocRecord{
				Project: project,
				RelPath: relPosix,
				Content: string(rawBytes),
			})
			if err != nil {
				return err
			}
			summary.DocsImported++
			return nil
		}

		if includeAssets && assetExtensions[ext] {
			if fileInfo.Size() > maxAssetBytes {
				summary.SkippedTooLarge++
				return nil
			}
			fileHandle, err := os.Open(currentPath)
			if err != nil {
				return nil
			}
			_, putErr := hub.PutBlob(ctx, project, relPosix, entry.Name(), "", fileHandle)
			_ = fileHandle.Close()
			if putErr != nil {
				return putErr
			}
			summary.AssetsImported++
		}
		return nil
	})
	return summary, err
}

func ImportTaskosFile(ctx context.Context, hub *store.HubStore, tasksJSONPath string) (int, error) {
	rawBytes, err := os.ReadFile(tasksJSONPath)
	if err != nil {
		return 0, fmt.Errorf("read tasks json: %w", err)
	}
	var envelope struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(rawBytes, &envelope); err != nil {
		return 0, fmt.Errorf("parse tasks json: %w", err)
	}

	imported := 0
	for _, rawTask := range envelope.Tasks {
		var fields struct {
			Code      string   `json:"code"`
			ID        string   `json:"id"`
			Project   string   `json:"project"`
			Series    string   `json:"series"`
			Milestone string   `json:"milestone"`
			Title     string   `json:"title"`
			Summary   string   `json:"summary"`
			Priority  string   `json:"priority"`
			Status    string   `json:"status"`
			DependsOn []string `json:"dependsOn"`
			Tags      []string `json:"tags"`
		}
		if err := json.Unmarshal(rawTask, &fields); err != nil {
			continue
		}
		code := strings.TrimSpace(fields.Code)
		if code == "" {
			code = strings.TrimSpace(fields.ID)
		}
		if code == "" {
			continue
		}
		_, err := hub.UpsertTask(ctx, store.TaskRecord{
			Code:        code,
			Project:     fields.Project,
			Series:      fields.Series,
			Milestone:   fields.Milestone,
			Title:       fields.Title,
			Summary:     fields.Summary,
			Priority:    fields.Priority,
			Status:      fields.Status,
			DependsOn:   fields.DependsOn,
			Tags:        fields.Tags,
			PayloadJSON: string(rawTask),
		})
		if err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}

func ImportRepo(ctx context.Context, hub *store.HubStore, project, repoRoot string, includeAssets bool) (ImportSummary,
	error) {
	absRepo, err := filepath.Abs(repoRoot)
	if err != nil {
		return ImportSummary{}, err
	}
	total := ImportSummary{Project: project, SourceDir: absRepo}

	docsDir := filepath.Join(absRepo, "docs")
	if info, err := os.Stat(docsDir); err == nil && info.IsDir() {
		docSummary, err := ImportDocsDirectory(ctx, hub, project, docsDir, includeAssets)
		if err != nil {
			return total, err
		}
		total.DocsImported += docSummary.DocsImported
		total.AssetsImported += docSummary.AssetsImported
		total.SkippedTooLarge += docSummary.SkippedTooLarge
	}

	for _, topDoc := range []string{"README.md", "ROADMAP.md", "CHANGELOG.md", "SECURITY.md"} {
		candidate := filepath.Join(absRepo, topDoc)
		if rawBytes, err := os.ReadFile(candidate); err == nil && len(rawBytes) <= maxMarkdownBytes {
			if _, upsertErr := hub.UpsertDoc(ctx, store.DocRecord{
				Project:  project,
				RelPath:  topDoc,
				Category: "root",
				Content:  string(rawBytes),
			}); upsertErr == nil {
				total.DocsImported++
			}
		}
	}

	for _, tasksCandidate := range []string{
		filepath.Join(absRepo, "docs", "tasks", "tasks.json"),
		filepath.Join(absRepo, "tasks.json"),
	} {
		if info, err := os.Stat(tasksCandidate); err == nil && !info.IsDir() {
			count, err := ImportTaskosFile(ctx, hub, tasksCandidate)
			if err != nil {
				return total, err
			}
			total.TasksImported += count
			break
		}
	}

	return total, nil
}
