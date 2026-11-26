package store_test

import (
	"context"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestProjectsCRUDAutoDiscoverAndRollups(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	ctx := context.Background()

	// Create tasks & docs before project exists -> ListProjects should auto-discover project slug
	_, err = hub.UpsertTask(ctx, store.TaskRecord{
		Code:      "PM-1",
		Project:   "alpha-app",
		Milestone: "Wave 1",
		Title:     "Done task",
		Priority:  "p1",
		Status:    "done",
	})
	if err != nil {
		t.Fatalf("upsert task 1: %v", err)
	}
	_, err = hub.UpsertTask(ctx, store.TaskRecord{
		Code:      "PM-2",
		Project:   "alpha-app",
		Milestone: "Wave 1",
		Title:     "Doing task",
		Priority:  "p0",
		Status:    "doing",
	})
	if err != nil {
		t.Fatalf("upsert task 2: %v", err)
	}
	_, err = hub.UpsertDoc(ctx, store.DocRecord{
		Project: "alpha-app",
		RelPath: "README.md",
		Content: "# Alpha App",
	})
	if err != nil {
		t.Fatalf("upsert doc: %v", err)
	}

	projects, err := hub.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Slug != "alpha-app" {
		t.Fatalf("expected auto-discovered alpha-app project, got %+v", projects)
	}
	if projects[0].TotalTasks != 2 || projects[0].DoneTasks != 1 || projects[0].InProgressTasks != 1 || projects[0].DocsCount != 1 {
		t.Fatalf("unexpected project metrics: %+v", projects[0])
	}
	if projects[0].ProgressPct != 50 {
		t.Fatalf("expected 50%% progress, got %.1f", projects[0].ProgressPct)
	}
	if len(projects[0].Milestones) != 1 || projects[0].Milestones[0].Name != "Wave 1" {
		t.Fatalf("unexpected milestones: %+v", projects[0].Milestones)
	}

	// Explicit UpsertProject updates metadata cleanly
	saved, err := hub.UpsertProject(ctx, store.ProjectRecord{
		Slug:         "alpha-app",
		Name:         "Alpha Core Platform",
		Description:  "Main Echo workspace",
		RepoPath:     `Z:\repo\alpha`,
		Status:       "active",
		Color:        "#10b981",
		DefaultAgent: "claude-code",
	})
	if err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if saved.Name != "Alpha Core Platform" || saved.DefaultAgent != "claude-code" {
		t.Fatalf("unexpected saved project: %+v", saved)
	}

	got, err := hub.GetProject(ctx, "alpha-app")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.Name != "Alpha Core Platform" || got.TotalTasks != 2 {
		t.Fatalf("unexpected get project: %+v", got)
	}
}
