package chatops

import (
	"regexp"
	"strings"
)

// RouteDomain identifies the operational vertical targeted by a WhatsApp message.
type RouteDomain string

const (
	DomainISP     RouteDomain = "ISP"
	DomainBilling RouteDomain = "Billing"
	DomainOps     RouteDomain = "Ops"
	DomainChat    RouteDomain = "Chat"
)

// IntentResult describes the classified bilingual (ID/EN) WhatsApp intent and target domain.
type IntentResult struct {
	Domain         RouteDomain `json:"domain"`
	Language       string      `json:"language"`
	NormalizedText string      `json:"normalizedText"`
	MatchedSignals []string    `json:"matchedSignals,omitempty"`
	Confidence     float64     `json:"confidence"`
}

var nonAlphaNumPattern = regexp.MustCompile(`[^a-z0-9/_-]+`)

type domainLexicon struct {
	domain RouteDomain
	id     []string
	en     []string
}

var bilingualLexicons = []domainLexicon{
	{
		domain: DomainOps,
		id: []string{
			"restart", "reboot", "deploy", "rollback", "backup", "restore",
			"kuras", "matikan", "nyalakan", "status server", "status sistem",
			"kesehatan nas", "cek ci", "jalankan", "otp",
		},
		en: []string{
			"restart", "reboot", "deploy", "rollback", "backup", "restore",
			"drain", "shutdown", "server status", "system status", "nas health",
			"ci status", "healthcheck", "flush", "exec", "otp",
		},
	},
	{
		domain: DomainBilling,
		id: []string{
			"tagihan", "kwitansi", "pembayaran", "bayar", "lunas", "faktur",
			"qris", "transfer", "saldo", "biaya", "langganan", "pph", "zakat",
			"jatuh tempo", "tunggakan",
		},
		en: []string{
			"invoice", "receipt", "billing", "payment", "pay", "paid",
			"subscription", "overdue", "balance", "refund", "charge", "tariff",
		},
	},
	{
		domain: DomainISP,
		id: []string{
			"internet", "putus", "lemot", "lambat", "los", "lampu merah",
			"modem", "ont", "onu", "olt", "mikrotik", "pppoe", "wifi",
			"sinyal", "gangguan", "kabel", "bandwidth", "paket data",
		},
		en: []string{
			"internet", "disconnect", "disconnected", "slow", "latency", "packet loss",
			"outage", "router", "modem", "ont", "onu", "olt", "mikrotik",
			"pppoe", "fiber", "bandwidth", "uplink", "ping",
		},
	},
}

var indonesianMarkers = []string{
	"cek", "tolong", "apakah", "bagaimana", "kenapa", "sudah", "belum",
	"saya", "kami", "tagihan", "kwitansi", "pembayaran", "bayar", "lunas",
	"putus", "lemot", "lambat", "gangguan", "lampu", "merah", "matikan",
	"nyalakan", "halo", "terima", "kasih", "selamat", "pagi", "siang", "malam",
}

var englishMarkers = []string{
	"please", "check", "what", "why", "how", "when", "where", "my", "our",
	"invoice", "receipt", "payment", "billing", "slow", "outage", "disconnected",
	"server", "health", "hello", "thanks", "morning", "evening",
}

// ClassifyIntent classifies an inbound Indonesian or English WhatsApp message into ISP, Billing, Ops, or Chat.
func ClassifyIntent(rawMessage string) IntentResult {
	lower := strings.ToLower(strings.TrimSpace(rawMessage))
	tokens := tokenizeWords(lower)
	normalized := strings.Join(tokens, " ")
	lang := detectLanguage(tokens)

	if normalized == "" {
		return IntentResult{
			Domain:         DomainChat,
			Language:       lang,
			NormalizedText: "",
			Confidence:     0.5,
		}
	}

	// Explicit slash commands map deterministically.
	firstToken := tokens[0]
	switch firstToken {
	case "/ops", "/status", "/restart", "/reboot", "/deploy", "/backup":
		return IntentResult{
			Domain:         DomainOps,
			Language:       lang,
			NormalizedText: normalized,
			MatchedSignals: []string{firstToken},
			Confidence:     0.99,
		}
	case "/billing", "/invoice", "/kwitansi", "/tagihan", "/pay":
		return IntentResult{
			Domain:         DomainBilling,
			Language:       lang,
			NormalizedText: normalized,
			MatchedSignals: []string{firstToken},
			Confidence:     0.99,
		}
	case "/isp", "/pppoe", "/mikrotik", "/ont", "/olt", "/ping":
		return IntentResult{
			Domain:         DomainISP,
			Language:       lang,
			NormalizedText: normalized,
			MatchedSignals: []string{firstToken},
			Confidence:     0.99,
		}
	}

	bestDomain := DomainChat
	bestScore := 0
	var bestSignals []string

	for _, lex := range bilingualLexicons {
		score, signals := scoreLexicon(normalized, tokens, lex)
		if score > bestScore {
			bestScore = score
			bestDomain = lex.domain
			bestSignals = signals
		}
	}

	if bestScore == 0 {
		return IntentResult{
			Domain:         DomainChat,
			Language:       lang,
			NormalizedText: normalized,
			Confidence:     0.65,
		}
	}

	conf := 0.70 + float64(bestScore)*0.08
	if conf > 0.98 {
		conf = 0.98
	}
	return IntentResult{
		Domain:         bestDomain,
		Language:       lang,
		NormalizedText: normalized,
		MatchedSignals: bestSignals,
		Confidence:     conf,
	}
}

func tokenizeWords(lower string) []string {
	cleaned := nonAlphaNumPattern.ReplaceAllString(lower, " ")
	return strings.Fields(cleaned)
}

func detectLanguage(tokens []string) string {
	idCount := 0
	enCount := 0
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	for _, m := range indonesianMarkers {
		if _, ok := tokenSet[m]; ok {
			idCount++
		}
	}
	for _, m := range englishMarkers {
		if _, ok := tokenSet[m]; ok {
			enCount++
		}
	}
	if idCount > enCount {
		return "id"
	}
	if enCount > idCount {
		return "en"
	}
	return "id"
}

func scoreLexicon(normalized string, tokens []string, lex domainLexicon) (int, []string) {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	seenSignal := make(map[string]struct{})
	var matched []string
	score := 0

	checkTerm := func(term string) {
		if _, exists := seenSignal[term]; exists {
			return
		}
		if strings.Contains(term, " ") {
			if strings.Contains(normalized, term) {
				seenSignal[term] = struct{}{}
				matched = append(matched, term)
				score += 2
			}
			return
		}
		if _, ok := tokenSet[term]; ok {
			seenSignal[term] = struct{}{}
			matched = append(matched, term)
			score++
		}
	}

	for _, term := range lex.id {
		checkTerm(term)
	}
	for _, term := range lex.en {
		checkTerm(term)
	}
	return score, matched
}
