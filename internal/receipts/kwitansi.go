package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	// PPhFinalRate is the 0.5% UMKM PP 55/2022 final income tax rate.
	PPhFinalRate = 0.005
	// ZakatMaalRate is the 2.5% Zakat Maal allocation rate.
	ZakatMaalRate = 0.025
)

// KwitansiInput defines the billing payload for generating an Indonesian Kwitansi receipt.
type KwitansiInput struct {
	InvoiceID       string
	CustomerName    string
	CustomerEmail   string
	CustomerPhone   string
	Description     string
	AmountIDR       int64
	PaidAt          time.Time
	IssuerName      string
	PaymentMethod   string
	IncludePPhFinal bool
	IncludeZakat    bool
}

// KwitansiReceipt holds the computed tax/zakat lines, SHA-256 receipt number, and rendered outputs.
type KwitansiReceipt struct {
	ReceiptNumber    string    `json:"receiptNumber"`
	ReceiptSHA256    string    `json:"receiptSha256"`
	InvoiceID        string    `json:"invoiceId"`
	CustomerName     string    `json:"customerName"`
	CustomerEmail    string    `json:"customerEmail,omitempty"`
	CustomerPhone    string    `json:"customerPhone,omitempty"`
	Description      string    `json:"description"`
	GrossAmountIDR   int64     `json:"grossAmountIdr"`
	PPhFinalIDR      int64     `json:"pphFinalIdr"`
	PPhFinalDisplay  string    `json:"pphFinalDisplay"`
	ZakatIDR         int64     `json:"zakatIdr"`
	ZakatDisplay     string    `json:"zakatDisplay"`
	NetAllocationIDR int64     `json:"netAllocationIdr"`
	PaidAt           time.Time `json:"paidAt"`
	IssuerName       string    `json:"issuerName"`
	PaymentMethod    string    `json:"paymentMethod"`
	TextSummary      string    `json:"textSummary"`
	HTMLDocument     string    `json:"htmlDocument"`
}

// GenerateKwitansi validates the input, calculates PPh Final 0.5% and Zakat 2.5% display lines,
// derives a deterministic SHA-256 receipt number, and renders WhatsApp text and Resend HTML views.
func GenerateKwitansi(in KwitansiInput) (KwitansiReceipt, error) {
	invoiceID := strings.TrimSpace(in.InvoiceID)
	customerName := strings.TrimSpace(in.CustomerName)
	description := strings.TrimSpace(in.Description)
	if invoiceID == "" {
		return KwitansiReceipt{}, errors.New("invoiceId is required")
	}
	if customerName == "" {
		return KwitansiReceipt{}, errors.New("customerName is required")
	}
	if description == "" {
		return KwitansiReceipt{}, errors.New("description is required")
	}
	if in.AmountIDR <= 0 {
		return KwitansiReceipt{}, errors.New("amountIdr must be positive")
	}

	paidAt := in.PaidAt
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	} else {
		paidAt = paidAt.UTC()
	}

	issuer := strings.TrimSpace(in.IssuerName)
	if issuer == "" {
		issuer = "PT MoonWitness Teknologi Indonesia"
	}
	method := strings.TrimSpace(in.PaymentMethod)
	if method == "" {
		method = "QRIS / Transfer Bank"
	}

	pphIDR := int64(math.Round(float64(in.AmountIDR) * PPhFinalRate))
	if !in.IncludePPhFinal && !in.IncludeZakat {
		// Default both transparent display lines on for Indonesian Kwitansi compliance.
		in.IncludePPhFinal = true
		in.IncludeZakat = true
	}
	if !in.IncludePPhFinal {
		pphIDR = 0
	}
	zakatIDR := int64(math.Round(float64(in.AmountIDR) * ZakatMaalRate))
	if !in.IncludeZakat {
		zakatIDR = 0
	}

	canonical := fmt.Sprintf("%s|%s|%d|%s", invoiceID, strings.ToLower(customerName), in.AmountIDR, paidAt.Format(time.RFC3339))
	sum := sha256.Sum256([]byte(canonical))
	fullHash := hex.EncodeToString(sum[:])
	shortHash := strings.ToUpper(fullHash[:12])
	receiptNo := fmt.Sprintf("KWT-%s-%s", paidAt.Format("20060102"), shortHash)

	pphLine := fmt.Sprintf("PPh Final 0.5%% (PP 55/2022): %s", FormatIDR(pphIDR))
	zakatLine := fmt.Sprintf("Alokasi Zakat 2.5%%: %s", FormatIDR(zakatIDR))
	netIDR := in.AmountIDR - pphIDR - zakatIDR

	receipt := KwitansiReceipt{
		ReceiptNumber:    receiptNo,
		ReceiptSHA256:    fullHash,
		InvoiceID:        invoiceID,
		CustomerName:     customerName,
		CustomerEmail:    strings.TrimSpace(in.CustomerEmail),
		CustomerPhone:    strings.TrimSpace(in.CustomerPhone),
		Description:      description,
		GrossAmountIDR:   in.AmountIDR,
		PPhFinalIDR:      pphIDR,
		PPhFinalDisplay:  pphLine,
		ZakatIDR:         zakatIDR,
		ZakatDisplay:     zakatLine,
		NetAllocationIDR: netIDR,
		PaidAt:           paidAt,
		IssuerName:       issuer,
		PaymentMethod:    method,
	}
	receipt.TextSummary = renderKwitansiText(receipt)
	receipt.HTMLDocument = renderKwitansiHTML(receipt)
	return receipt, nil
}

// FormatIDR formats an integer Rupiah value with Indonesian dot thousand separators (e.g. "Rp 1.500.000").
func FormatIDR(amount int64) string {
	negative := amount < 0
	if negative {
		amount = -amount
	}
	raw := fmt.Sprintf("%d", amount)
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	formatted := "Rp " + strings.Join(parts, ".")
	if negative {
		return "-" + formatted
	}
	return formatted
}

func renderKwitansiText(r KwitansiReceipt) string {
	var b strings.Builder
	b.WriteString("*KWITANSI PEMBAYARAN RESMI*\n")
	b.WriteString(fmt.Sprintf("No. Kwitansi: %s\n", r.ReceiptNumber))
	b.WriteString(fmt.Sprintf("No. Tagihan: %s\n", r.InvoiceID))
	b.WriteString(fmt.Sprintf("Tanggal Lunas: %s WIB\n", r.PaidAt.Add(7*time.Hour).Format("02 Jan 2006 15:04")))
	b.WriteString(fmt.Sprintf("Terima Dari: %s\n", r.CustomerName))
	b.WriteString(fmt.Sprintf("Untuk Pembayaran: %s\n", r.Description))
	b.WriteString(fmt.Sprintf("Metode: %s\n", r.PaymentMethod))
	b.WriteString(fmt.Sprintf("Nominal Lunas: %s\n", FormatIDR(r.GrossAmountIDR)))
	b.WriteString(fmt.Sprintf("- %s\n", r.PPhFinalDisplay))
	b.WriteString(fmt.Sprintf("- %s\n", r.ZakatDisplay))
	b.WriteString(fmt.Sprintf("SHA-256: %s\n", r.ReceiptSHA256))
	b.WriteString(fmt.Sprintf("Penerbit: %s", r.IssuerName))
	return b.String()
}

func renderKwitansiHTML(r KwitansiReceipt) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="id">
<head><meta charset="utf-8"><title>Kwitansi %s</title></head>
<body>
  <h2>KWITANSI PEMBAYARAN RESMI</h2>
  <p><strong>No. Kwitansi:</strong> %s</p>
  <p><strong>No. Invoice:</strong> %s</p>
  <p><strong>Telah Terima Dari:</strong> %s</p>
  <p><strong>Keterangan:</strong> %s</p>
  <p><strong>Metode Pembayaran:</strong> %s</p>
  <p><strong>Jumlah Pembayaran:</strong> %s</p>
  <ul>
    <li>%s</li>
    <li>%s</li>
  </ul>
  <p><small>SHA-256 Integrity: <code>%s</code></small></p>
  <p><small>Diterbitkan oleh %s pada %s WIB</small></p>
</body>
</html>`,
		html.EscapeString(r.ReceiptNumber),
		html.EscapeString(r.ReceiptNumber),
		html.EscapeString(r.InvoiceID),
		html.EscapeString(r.CustomerName),
		html.EscapeString(r.Description),
		html.EscapeString(r.PaymentMethod),
		html.EscapeString(FormatIDR(r.GrossAmountIDR)),
		html.EscapeString(r.PPhFinalDisplay),
		html.EscapeString(r.ZakatDisplay),
		html.EscapeString(r.ReceiptSHA256),
		html.EscapeString(r.IssuerName),
		r.PaidAt.Add(7*time.Hour).Format("02 Jan 2006 15:04"),
	)
}

// WhatsAppSender abstracts outbound WhatsApp text dispatch.
type WhatsAppSender interface {
	SendWhatsAppText(ctx context.Context, phone, message string) (string, error)
}

// DeliveryConfig configures the WhatsApp + Resend Kwitansi auto-delivery adapter.
type DeliveryConfig struct {
	WhatsApp      WhatsAppSender
	ResendAPIBase string
	ResendAPIKey  string
	ResendFrom    string
	HTTPClient    *http.Client
}

// DeliveryResult summarizes the dual-channel Kwitansi delivery outcome.
type DeliveryResult struct {
	ReceiptNumber     string `json:"receiptNumber"`
	WhatsAppDelivered bool   `json:"whatsAppDelivered"`
	WhatsAppMessageID string `json:"whatsAppMessageId,omitempty"`
	EmailDelivered    bool   `json:"emailDelivered"`
	EmailID           string `json:"emailId,omitempty"`
}

// DeliveryAdapter dispatches generated Kwitansi receipts over WhatsApp and Resend email.
type DeliveryAdapter struct {
	wa            WhatsAppSender
	resendAPIBase string
	resendAPIKey  string
	resendFrom    string
	httpClient    *http.Client
}

// NewDeliveryAdapter initializes a Kwitansi auto-delivery adapter.
func NewDeliveryAdapter(cfg DeliveryConfig) *DeliveryAdapter {
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.ResendAPIBase), "/")
	if apiBase == "" {
		apiBase = "https://api.resend.com"
	}
	from := strings.TrimSpace(cfg.ResendFrom)
	if from == "" {
		from = "billing@moonwitness.id"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &DeliveryAdapter{
		wa:            cfg.WhatsApp,
		resendAPIBase: apiBase,
		resendAPIKey:  strings.TrimSpace(cfg.ResendAPIKey),
		resendFrom:    from,
		httpClient:    client,
	}
}

// Deliver dispatches the Kwitansi receipt to the customer's WhatsApp phone and/or email via Resend.
func (a *DeliveryAdapter) Deliver(ctx context.Context, receipt KwitansiReceipt) (DeliveryResult, error) {
	if strings.TrimSpace(receipt.ReceiptNumber) == "" {
		return DeliveryResult{}, errors.New("receiptNumber is required")
	}
	res := DeliveryResult{ReceiptNumber: receipt.ReceiptNumber}
	var errs []string

	if phone := strings.TrimSpace(receipt.CustomerPhone); phone != "" && a.wa != nil {
		msgID, err := a.wa.SendWhatsAppText(ctx, phone, receipt.TextSummary)
		if err != nil {
			errs = append(errs, fmt.Sprintf("whatsapp delivery: %v", err))
		} else {
			res.WhatsAppDelivered = true
			res.WhatsAppMessageID = msgID
		}
	}

	if email := strings.TrimSpace(receipt.CustomerEmail); email != "" && a.resendAPIKey != "" {
		emailID, err := a.sendResendEmail(ctx, email, receipt)
		if err != nil {
			errs = append(errs, fmt.Sprintf("resend delivery: %v", err))
		} else {
			res.EmailDelivered = true
			res.EmailID = emailID
		}
	}

	if !res.WhatsAppDelivered && !res.EmailDelivered {
		if len(errs) > 0 {
			return res, errors.New(strings.Join(errs, "; "))
		}
		return res, errors.New("no delivery channel configured or recipient contact missing")
	}
	if len(errs) > 0 {
		return res, errors.New(strings.Join(errs, "; "))
	}
	return res, nil
}

func (a *DeliveryAdapter) sendResendEmail(ctx context.Context, recipient string, receipt KwitansiReceipt) (string, error) {
	if !strings.Contains(recipient, "@") || strings.ContainsAny(recipient, "\r\n") {
		return "", fmt.Errorf("invalid recipient email %q", recipient)
	}
	payload := map[string]any{
		"from":    a.resendFrom,
		"to":      []string{recipient},
		"subject": fmt.Sprintf("Kwitansi Pembayaran %s - %s", receipt.ReceiptNumber, receipt.IssuerName),
		"text":    receipt.TextSummary,
		"html":    receipt.HTMLDocument,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.resendAPIBase+"/emails", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.resendAPIKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("resend HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return parsed.ID, nil
}
