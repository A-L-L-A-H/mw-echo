package chatops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassifyIntent_BilingualIDAndEN(t *testing.T) {
	cases := []struct {
		name       string
		msg        string
		wantDomain RouteDomain
		wantLang   string
	}{
		{
			name:       "Indonesian ISP outage",
			msg:        "Halo min, internet saya putus lampu merah LOS di modem ONT",
			wantDomain: DomainISP,
			wantLang:   "id",
		},
		{
			name:       "English ISP latency",
			msg:        "Please check my fiber router, high latency and packet loss on PPPoE",
			wantDomain: DomainISP,
			wantLang:   "en",
		},
		{
			name:       "Indonesian Billing Kwitansi",
			msg:        "Tolong kirim kwitansi pembayaran tagihan bulan ini sudah lunas via QRIS",
			wantDomain: DomainBilling,
			wantLang:   "id",
		},
		{
			name:       "English Billing Invoice",
			msg:        "Where is my monthly subscription invoice and payment receipt?",
			wantDomain: DomainBilling,
			wantLang:   "en",
		},
		{
			name:       "Indonesian Ops NAS & Server",
			msg:        "Tolong cek kesehatan nas dan restart layanan gateway",
			wantDomain: DomainOps,
			wantLang:   "id",
		},
		{
			name:       "English Ops Deploy",
			msg:        "/deploy mw-echo-prod",
			wantDomain: DomainOps,
			wantLang:   "id",
		},
		{
			name:       "General Chat fallback",
			msg:        "Selamat pagi, semoga hari ini menyenangkan!",
			wantDomain: DomainChat,
			wantLang:   "id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIntent(tc.msg)
			if got.Domain != tc.wantDomain {
				t.Fatalf("ClassifyIntent(%q).Domain = %s, want %s (signals=%v)", tc.msg, got.Domain, tc.wantDomain,
					got.MatchedSignals)
			}
			if got.Language != tc.wantLang {
				t.Fatalf("ClassifyIntent(%q).Language = %s, want %s", tc.msg, got.Language, tc.wantLang)
			}
		})
	}
}

func TestBridge_R1ReadOnlyAndR3FailClosedOTP(t *testing.T) {
	verifier := &StaticOTPVerifier{
		ExpectedBySender: map[string]string{
			"628111222333": "739204",
		},
	}
	bridge := NewBridge(BridgeConfig{
		OTPVerifier: verifier,
	})

	// 1. R1 read-only succeeds without OTP
	r1Out, err := bridge.Execute(context.Background(), "628111222333", "/status")
	if err != nil {
		t.Fatalf("R1 /status unexpected error: %v", err)
	}
	if r1Out.RiskTier != RiskR1ReadOnly || !r1Out.Authorized {
		t.Fatalf("unexpected R1 outcome: %+v", r1Out)
	}

	// 2. R3 without OTP fails closed
	r3Missing, err := bridge.Execute(context.Background(), "628111222333", "/restart mw-core")
	if !errors.Is(err, ErrOTPRequired) {
		t.Fatalf("expected ErrOTPRequired, got err=%v outcome=%+v", err, r3Missing)
	}
	if r3Missing.Authorized {
		t.Fatalf("R3 without OTP must not be authorized")
	}

	// 3. R3 with wrong OTP fails closed
	r3Bad, err := bridge.Execute(context.Background(), "628111222333", "/restart mw-core --otp 000000")
	if !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("expected ErrOTPInvalid, got err=%v outcome=%+v", err, r3Bad)
	}
	if r3Bad.Authorized || r3Bad.OTPVerified {
		t.Fatalf("R3 with invalid OTP must not be authorized")
	}

	// 4. R3 with valid OTP executes
	r3OK, err := bridge.Execute(context.Background(), "628111222333", "/restart mw-core --otp 739204")
	if err != nil {
		t.Fatalf("R3 with valid OTP failed: %v", err)
	}
	if !r3OK.Authorized || !r3OK.OTPVerified || !strings.Contains(r3OK.Output, "restarted mw-core") {
		t.Fatalf("unexpected R3 success outcome: %+v", r3OK)
	}
}

type stubVoiceTransport struct {
	pttMime   string
	pttAudio  []byte
	pttErr    error
	textMsg   string
	textErr   error
	pttCalls  int
	textCalls int
}

func (s *stubVoiceTransport) SendVoicePTT(_ context.Context, _, mimeType string, audio []byte) (string, error) {
	s.pttCalls++
	s.pttMime = mimeType
	s.pttAudio = audio
	if s.pttErr != nil {
		return "", s.pttErr
	}
	return "wa_ptt_101", nil
}

func (s *stubVoiceTransport) SendTextFallback(_ context.Context, _, text string) (string, error) {
	s.textCalls++
	s.textMsg = text
	if s.textErr != nil {
		return "", s.textErr
	}
	return "wa_txt_202", nil
}

func TestVoiceTTSResponder_OGGOpusPTTAndAutomaticTextFallback(t *testing.T) {
	fakeOggOpus := []byte("OggS\x00\x02OpusHead-mock-frame")
	studioHealthy := true
	studioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !studioHealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"tts engine offline"}`))
			return
		}
		w.Header().Set("Content-Type", oggOpusMimeType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeOggOpus)
	}))
	defer studioSrv.Close()

	transport := &stubVoiceTransport{}
	responder, err := NewVoiceTTSResponder(VoiceTTSConfig{
		StudioBaseURL: studioSrv.URL,
		StudioAPIKey:  "studio_secret",
		Transport:     transport,
		HTTPClient:    studioSrv.Client(),
	})
	if err != nil {
		t.Fatalf("NewVoiceTTSResponder error: %v", err)
	}

	// 1. Happy path: OGG Opus PTT voice note delivered
	out, err := responder.Respond(context.Background(), "628123456789", "Tagihan Anda sudah lunas.")
	if err != nil {
		t.Fatalf("Respond PTT error: %v", err)
	}
	if out.Modality != "voice_ptt" || out.FallbackUsed || out.MessageID != "wa_ptt_101" {
		t.Fatalf("unexpected PTT outcome: %+v", out)
	}
	if transport.pttMime != oggOpusMimeType || len(transport.pttAudio) != len(fakeOggOpus) {
		t.Fatalf("unexpected PTT payload: mime=%s bytes=%d", transport.pttMime, len(transport.pttAudio))
	}

	// 2. Fallback path: mw-studio returns 503 -> automatic text fallback
	studioHealthy = false
	fallbackOut, err := responder.Respond(context.Background(), "628123456789", "Router utama sedang direstart.")
	if err != nil {
		t.Fatalf("Respond fallback error: %v", err)
	}
	if fallbackOut.Modality != "text_fallback" || !fallbackOut.FallbackUsed || fallbackOut.MessageID != "wa_txt_202" {
		t.Fatalf("expected text_fallback outcome, got: %+v", fallbackOut)
	}
	if transport.textMsg != "Router utama sedang direstart." {
		t.Fatalf("unexpected fallback text message: %s", transport.textMsg)
	}
}

func TestEmailTicketBridge_CloudflareInboundToMWXVAndResendACK(t *testing.T) {
	var receivedTicket MWXVTicketPayload
	mwxvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedTicket)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"mwxv_tkt_881"}`))
	}))
	defer mwxvSrv.Close()

	var resendPayload map[string]any
	resendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&resendPayload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"re_ack_442"}`))
	}))
	defer resendSrv.Close()

	bridge := NewEmailTicketBridge(EmailTicketBridgeConfig{
		MWXVTicketEndpoint: mwxvSrv.URL + "/api/tickets",
		MWXVAuthToken:      "mwxv_token_abc",
		ResendAPIBase:      resendSrv.URL,
		ResendAPIKey:       "re_key_xyz",
		ResendFrom:         "helpdesk@moonwitness.id",
		HTTPClient:         mwxvSrv.Client(),
	})

	outcome, err := bridge.ProcessInbound(context.Background(), CloudflareInboundEmail{
		MessageID: "<cf-msg-01@example.id>",
		From:      "pelanggan@корпорация.id",
		To:        "support@moonwitness.id",
		Subject:   "Internet Putus Lampu Merah LOS",
		Text:      "Mohon bantuan segera, koneksi fiber kantor kami putus sejak pagi.",
	})
	if err != nil {
		t.Fatalf("ProcessInbound error: %v", err)
	}
	if outcome.MWXVTicketID != "mwxv_tkt_881" {
		t.Fatalf("MWXVTicketID = %s, want mwxv_tkt_881", outcome.MWXVTicketID)
	}
	if !outcome.ACKSent || outcome.ResendEmailID != "re_ack_442" {
		t.Fatalf("expected Resend ACK sent, got %+v", outcome)
	}
	if receivedTicket.Category != DomainISP || receivedTicket.Priority != "P1" {
		t.Fatalf("expected ISP P1 ticket classification, got category=%s priority=%s", receivedTicket.Category,
			receivedTicket.Priority)
	}
	if !strings.Contains(resendPayload["subject"].(string), outcome.Ticket.TicketRef) {
		t.Fatalf("Resend ACK subject missing ticket reference: %v", resendPayload["subject"])
	}
}
