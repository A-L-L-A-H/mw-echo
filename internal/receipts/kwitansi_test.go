package receipts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubWhatsAppSender struct {
	lastPhone string
	lastText  string
	msgID     string
	err       error
}

func (s *stubWhatsAppSender) SendWhatsAppText(_ context.Context, phone, message string) (string, error) {
	s.lastPhone = phone
	s.lastText = message
	if s.err != nil {
		return "", s.err
	}
	return s.msgID, nil
}

func TestGenerateKwitansi_ComputesPPhZakatAndSHA256(t *testing.T) {
	paidAt := time.Date(2026, 11, 27, 3, 15, 0, 0, time.UTC)
	receipt, err := GenerateKwitansi(KwitansiInput{
		InvoiceID:     "INV-2026-0042",
		CustomerName:  "Budi Santoso",
		CustomerEmail: "budi@example.id",
		CustomerPhone: "6281234567890",
		Description:   "Langganan Dedicated Fiber 100 Mbps - November 2026",
		AmountIDR:     2_000_000,
		PaidAt:        paidAt,
	})
	if err != nil {
		t.Fatalf("GenerateKwitansi error: %v", err)
	}

	if receipt.PPhFinalIDR != 10_000 {
		t.Fatalf("PPhFinalIDR = %d, want 10000", receipt.PPhFinalIDR)
	}
	if receipt.ZakatIDR != 50_000 {
		t.Fatalf("ZakatIDR = %d, want 50000", receipt.ZakatIDR)
	}
	if receipt.NetAllocationIDR != 1_940_000 {
		t.Fatalf("NetAllocationIDR = %d, want 1940000", receipt.NetAllocationIDR)
	}
	if !strings.HasPrefix(receipt.ReceiptNumber, "KWT-20261127-") {
		t.Fatalf("unexpected ReceiptNumber format: %s", receipt.ReceiptNumber)
	}
	if len(receipt.ReceiptSHA256) != 64 {
		t.Fatalf("expected 64-char SHA-256 hex, got %q", receipt.ReceiptSHA256)
	}
	if !strings.Contains(receipt.TextSummary, "PPh Final 0.5% (PP 55/2022): Rp 10.000") {
		t.Fatalf("TextSummary missing PPh Final display line:\n%s", receipt.TextSummary)
	}
	if !strings.Contains(receipt.TextSummary, "Alokasi Zakat 2.5%: Rp 50.000") {
		t.Fatalf("TextSummary missing Zakat display line:\n%s", receipt.TextSummary)
	}
	if !strings.Contains(receipt.HTMLDocument, receipt.ReceiptSHA256) {
		t.Fatalf("HTMLDocument missing SHA-256 integrity hash")
	}

	// Deterministic SHA-256 check
	again, err := GenerateKwitansi(KwitansiInput{
		InvoiceID:    "INV-2026-0042",
		CustomerName: "Budi Santoso",
		Description:  "Langganan Dedicated Fiber 100 Mbps - November 2026",
		AmountIDR:    2_000_000,
		PaidAt:       paidAt,
	})
	if err != nil {
		t.Fatalf("second GenerateKwitansi error: %v", err)
	}
	if receipt.ReceiptNumber != again.ReceiptNumber || receipt.ReceiptSHA256 != again.ReceiptSHA256 {
		t.Fatalf("expected deterministic receipt number & hash, got %s vs %s", receipt.ReceiptNumber, again.ReceiptNumber)
	}
}

func TestDeliveryAdapter_WhatsAppAndResend(t *testing.T) {
	var capturedAuth string
	var capturedPayload map[string]any
	resendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"re_msg_991"}`))
	}))
	defer resendSrv.Close()

	waStub := &stubWhatsAppSender{msgID: "wa_msg_772"}
	adapter := NewDeliveryAdapter(DeliveryConfig{
		WhatsApp:      waStub,
		ResendAPIBase: resendSrv.URL,
		ResendAPIKey:  "re_live_key_123",
		ResendFrom:    "kwitansi@moonwitness.id",
		HTTPClient:    resendSrv.Client(),
	})

	receipt, err := GenerateKwitansi(KwitansiInput{
		InvoiceID:     "INV-99",
		CustomerName:  "Siti Aminah",
		CustomerEmail: "siti@example.id",
		CustomerPhone: "6281199887766",
		Description:   "Paket Bisnis 50 Mbps",
		AmountIDR:     1_000_000,
	})
	if err != nil {
		t.Fatalf("GenerateKwitansi error: %v", err)
	}

	result, err := adapter.Deliver(context.Background(), receipt)
	if err != nil {
		t.Fatalf("Deliver error: %v", err)
	}
	if !result.WhatsAppDelivered || result.WhatsAppMessageID != "wa_msg_772" {
		t.Fatalf("unexpected WhatsApp delivery result: %+v", result)
	}
	if !result.EmailDelivered || result.EmailID != "re_msg_991" {
		t.Fatalf("unexpected Resend email delivery result: %+v", result)
	}
	if waStub.lastPhone != "6281199887766" || !strings.Contains(waStub.lastText, receipt.ReceiptNumber) {
		t.Fatalf("WhatsApp payload mismatch: phone=%s text=%s", waStub.lastPhone, waStub.lastText)
	}
	if capturedAuth != "Bearer re_live_key_123" {
		t.Fatalf("unexpected Resend auth header: %s", capturedAuth)
	}
	if capturedPayload["from"] != "kwitansi@moonwitness.id" {
		t.Fatalf("unexpected Resend from field: %v", capturedPayload["from"])
	}
}
