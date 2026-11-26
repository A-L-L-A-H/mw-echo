package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func openTestManager(t *testing.T) (*Manager, *store.HubStore) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	mgr, err := NewManager(hub, Options{ProbeTimeout: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("new fleet manager: %v", err)
	}
	return mgr, hub
}

func TestARSYGrantBudgetAndRiskEnforcement(t *testing.T) {
	mgr, _ := openTestManager(t)
	ctx := context.Background()

	// R3/R4 must be denied without an explicit active grant.
	deniedR3, err := mgr.CheckAndConsume(ctx, "agent-alpha", "deploy.prod", "R3", "cluster/prod", 1)
	if err != nil {
		t.Fatalf("check R3 without grant: %v", err)
	}
	if deniedR3.Allowed || deniedR3.Decision != "deny" {
		t.Fatalf("expected R3 denial without grant, got %+v", deniedR3)
	}

	// Issue a grant with budget 5 for R3.
	grant, err := mgr.IssueGrant(ctx, GrantInput{
		Subject:     "agent-alpha",
		Capability:  "deploy.prod",
		RiskTier:    "R3",
		BudgetLimit: 5,
	})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}

	// Consume 3 units -> allowed, 2 remaining.
	allowedFirst, err := mgr.CheckAndConsume(ctx, "agent-alpha", "deploy.prod", "R3", "cluster/prod", 3)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if !allowedFirst.Allowed || allowedFirst.BudgetRemaining != 2 || allowedFirst.GrantID != grant.ID {
		t.Fatalf("expected allow with 2 remaining, got %+v", allowedFirst)
	}

	// R4 still denied when grant only covers R3.
	deniedR4, err := mgr.CheckAndConsume(ctx, "agent-alpha", "deploy.prod", "R4", "cluster/prod", 1)
	if err != nil {
		t.Fatalf("check R4 against R3 grant: %v", err)
	}
	if deniedR4.Allowed {
		t.Fatalf("expected R4 denial against R3 grant, got %+v", deniedR4)
	}

	// Consume 3 more units -> exceeds budget (3+3 > 5).
	exceeded, err := mgr.CheckAndConsume(ctx, "agent-alpha", "deploy.prod", "R3", "cluster/prod", 3)
	if err != nil {
		t.Fatalf("budget exceeded check: %v", err)
	}
	if exceeded.Allowed {
		t.Fatalf("expected budget denial, got %+v", exceeded)
	}

	// Revoke grant -> subsequent 1-unit request denied.
	if err := mgr.RevokeGrant(ctx, grant.ID); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	afterRevoke, err := mgr.CheckAndConsume(ctx, "agent-alpha", "deploy.prod", "R3", "cluster/prod", 1)
	if err != nil {
		t.Fatalf("check after revoke: %v", err)
	}
	if afterRevoke.Allowed {
		t.Fatalf("expected denial after revoke, got %+v", afterRevoke)
	}
}

func TestARSYMerkleAuditVerificationAndTamperDetection(t *testing.T) {
	mgr, hub := openTestManager(t)
	ctx := context.Background()

	_, err := mgr.IssueGrant(ctx, GrantInput{
		Subject:     "agent-beta",
		Capability:  "db.migrate",
		RiskTier:    "R2",
		BudgetLimit: 10,
	})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}

	if _, err := mgr.CheckAndConsume(ctx, "agent-beta", "db.migrate", "R2", "sqlite/main", 2); err != nil {
		t.Fatalf("consume 1: %v", err)
	}
	if _, err := mgr.CheckAndConsume(ctx, "agent-beta", "db.migrate", "R2", "sqlite/main", 3); err != nil {
		t.Fatalf("consume 2: %v", err)
	}

	validCheck, err := mgr.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify valid chain: %v", err)
	}
	if !validCheck.Valid || validCheck.Count != 2 || validCheck.BrokenSeq != 0 {
		t.Fatalf("expected valid chain of 2, got %+v", validCheck)
	}

	// Tamper with row 1 decision directly in SQLite.
	if _, err := hub.DB().ExecContext(ctx, `UPDATE arsy_merkle_audit SET decision = 'tampered' WHERE seq = 1`); err != nil {
		t.Fatalf("tamper audit row: %v", err)
	}

	tamperedCheck, err := mgr.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify tampered chain: %v", err)
	}
	if tamperedCheck.Valid || tamperedCheck.BrokenSeq != 1 {
		t.Fatalf("expected broken chain at seq 1, got %+v", tamperedCheck)
	}
}

func TestConcurrentFleetProbing(t *testing.T) {
	mgr, _ := openTestManager(t)
	ctx := context.Background()

	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer healthyServer.Close()

	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failingServer.Close()

	if _, err := mgr.UpsertService(ctx, "mw-lab", "MW Lab", healthyServer.URL); err != nil {
		t.Fatalf("upsert healthy service: %v", err)
	}
	if _, err := mgr.UpsertService(ctx, "mw-mission", "MW Mission", failingServer.URL); err != nil {
		t.Fatalf("upsert failing service: %v", err)
	}

	report, err := mgr.ProbeFleet(ctx)
	if err != nil {
		t.Fatalf("probe fleet: %v", err)
	}
	if report.TotalCount < 11 {
		t.Fatalf("expected at least 11 fleet services, got %d", report.TotalCount)
	}
	if report.OnlineCount != 1 {
		t.Fatalf("expected exactly 1 online service, got %d", report.OnlineCount)
	}

	var sawHealthy, sawFailing bool
	for _, svc := range report.Services {
		if svc.Key == "mw-lab" {
			sawHealthy = true
			if !svc.Online || svc.StatusCode != http.StatusOK {
				t.Fatalf("expected mw-lab online 200, got %+v", svc)
			}
		}
		if svc.Key == "mw-mission" {
			sawFailing = true
			if svc.Online || svc.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected mw-mission offline 503, got %+v", svc)
			}
		}
	}
	if !sawHealthy || !sawFailing {
		t.Fatalf("missing expected probed services: healthy=%v failing=%v", sawHealthy, sawFailing)
	}
}
