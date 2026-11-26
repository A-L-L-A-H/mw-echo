package comms_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/comms"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func newInboundHarness(t *testing.T, secret string) *echo.Echo {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	mgr, err := comms.NewManager(hub, comms.Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	mgr.SetInboundEmailSecret(secret)
	e := echo.New()
	mgr.RegisterRoutes(e.Group("/api"))
	return e
}

func postInbound(e *echo.Echo, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/comms/inbound-email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestInboundEmailAuthAndTicket(t *testing.T) {
	const secret = "s3cret"
	e := newInboundHarness(t, secret)
	payload := `{"from":"a@b.com","to":"support@x.com","subject":"Help","text":"broken"}`

	if rec := postInbound(e, payload, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: got %d", rec.Code)
	}
	if rec := postInbound(e, payload, map[string]string{"X-Webhook-Secret": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad secret: got %d", rec.Code)
	}
	if rec := postInbound(e, payload, map[string]string{"X-Webhook-Secret": secret}); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"priority":"P1"`) {
		t.Fatalf("shared secret: got %d %s", rec.Code, rec.Body.String())
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if rec := postInbound(e, payload, map[string]string{"X-Webhook-Signature": sig}); rec.Code != http.StatusCreated {
		t.Fatalf("hmac: got %d", rec.Code)
	}
	if rec := postInbound(e, `{"from":"","subject":""}`, map[string]string{"X-Webhook-Secret": secret}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid: got %d", rec.Code)
	}
}

func TestInboundEmailFailsClosedWithoutSecret(t *testing.T) {
	t.Setenv("MW_ECHO_INBOUND_EMAIL_SECRET", "")
	e := newInboundHarness(t, "")
	rec := postInbound(e, `{"from":"a@b.com","subject":"x"}`, map[string]string{"X-Webhook-Secret": ""})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}
