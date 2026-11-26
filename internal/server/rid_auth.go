package server

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultJWKSFetchTimeout = 10 * time.Second
	defaultJWKSCacheTTL     = 5 * time.Minute
	maxJWKSResponseBytes    = 1 << 20
)

// RIDVerifierConfig configures the pure-Go mw-rid RS256 JWKS verifier.
type RIDVerifierConfig struct {
	JWKSURL    string
	Issuer     string
	Audience   string
	HTTPClient *http.Client
	CacheTTL   time.Duration
	Now        func() time.Time
}

// RIDClaims holds validated claims extracted from an mw-rid RS256 access token.
type RIDClaims struct {
	Subject  string   `json:"sub"`
	Issuer   string   `json:"iss"`
	Audience []string `json:"aud,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
	TokenUse string   `json:"token_use,omitempty"`
	KeyID    string   `json:"kid,omitempty"`
	Expires  int64    `json:"exp"`
}

// RIDVerifier verifies mw-rid RS256 access tokens against a remote JWKS endpoint.
type RIDVerifier struct {
	jwksURL    string
	issuer     string
	audience   string
	httpClient *http.Client
	cacheTTL   time.Duration
	nowFunc    func() time.Time

	mu        sync.RWMutex
	keysByKID map[string]*rsa.PublicKey
	fetchedAt time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type jwtPayload struct {
	Issuer   string          `json:"iss"`
	Subject  string          `json:"sub"`
	Audience json.RawMessage `json:"aud"`
	Exp      *float64        `json:"exp"`
	Nbf      *float64        `json:"nbf"`
	TokenUse string          `json:"token_use"`
	AtHash   string          `json:"at_hash"`
	CHash    string          `json:"c_hash"`
	Scope    string          `json:"scope"`
	ClientID string          `json:"client_id"`
}

type jwksDocument struct {
	Keys []jwkEntry `json:"keys"`
}

type jwkEntry struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// NewRIDVerifier constructs a pure-Go RS256 JWKS verifier for mw-rid access tokens.
func NewRIDVerifier(cfg RIDVerifierConfig) (*RIDVerifier, error) {
	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if jwksURL == "" {
		return nil, errors.New("rid verifier: jwks url is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultJWKSFetchTimeout}
	}
	cacheTTL := cfg.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultJWKSCacheTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &RIDVerifier{
		jwksURL:    jwksURL,
		issuer:     strings.TrimSpace(cfg.Issuer),
		audience:   strings.TrimSpace(cfg.Audience),
		httpClient: client,
		cacheTTL:   cacheTTL,
		nowFunc:    nowFn,
		keysByKID:  make(map[string]*rsa.PublicKey),
	}, nil
}

// VerifyAccessToken validates a compact JWS RS256 access token enforcing ADR-001 rules.
func (v *RIDVerifier) VerifyAccessToken(ctx context.Context, rawToken string) (RIDClaims, error) {
	trimmedToken := strings.TrimSpace(rawToken)
	if trimmedToken == "" {
		return RIDClaims{}, errors.New("empty bearer token")
	}

	segments := strings.Split(trimmedToken, ".")
	if len(segments) != 3 {
		return RIDClaims{}, errors.New("malformed jwt: expected 3 segments")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return RIDClaims{}, fmt.Errorf("decode jwt header: %w", err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return RIDClaims{}, fmt.Errorf("parse jwt header: %w", err)
	}

	// ADR-001: strictly require RS256 (blocks HS256, none, and symmetric confusion).
	if hdr.Alg != "RS256" {
		return RIDClaims{}, fmt.Errorf("unsupported jwt alg %q: only RS256 is permitted", hdr.Alg)
	}
	if normalizedTyp := strings.ToLower(strings.TrimSpace(hdr.Typ)); normalizedTyp != "" {
		if normalizedTyp != "at+jwt" && normalizedTyp != "application/at+jwt" && normalizedTyp != "jwt" {
			return RIDClaims{}, fmt.Errorf("invalid access token typ header %q", hdr.Typ)
		}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return RIDClaims{}, fmt.Errorf("decode jwt payload: %w", err)
	}
	var payload jwtPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return RIDClaims{}, fmt.Errorf("parse jwt payload: %w", err)
	}

	// ADR-001: reject OIDC ID tokens.
	if strings.EqualFold(strings.TrimSpace(payload.TokenUse), "id") {
		return RIDClaims{}, errors.New("oidc id tokens are rejected; access token required")
	}
	if strings.TrimSpace(payload.AtHash) != "" || strings.TrimSpace(payload.CHash) != "" {
		return RIDClaims{}, errors.New("oidc id tokens containing at_hash or c_hash are rejected")
	}

	signatureBytes, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		return RIDClaims{}, fmt.Errorf("decode jwt signature: %w", err)
	}

	pubKey, err := v.resolveKey(ctx, strings.TrimSpace(hdr.Kid))
	if err != nil {
		return RIDClaims{}, err
	}

	signingInput := segments[0] + "." + segments[1]
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], signatureBytes); err != nil {
		return RIDClaims{}, errors.New("invalid jwt signature")
	}

	nowUnix := v.nowFunc().UTC().Unix()
	if payload.Exp == nil {
		return RIDClaims{}, errors.New("jwt missing required exp claim")
	}
	expUnix := int64(*payload.Exp)
	if nowUnix >= expUnix {
		return RIDClaims{}, errors.New("jwt expired")
	}
	if payload.Nbf != nil && nowUnix < int64(*payload.Nbf) {
		return RIDClaims{}, errors.New("jwt not yet valid (nbf)")
	}

	if v.issuer != "" && payload.Issuer != v.issuer {
		return RIDClaims{}, fmt.Errorf("jwt issuer mismatch: got %q", payload.Issuer)
	}

	audiences, err := parseAudienceClaim(payload.Audience)
	if err != nil {
		return RIDClaims{}, fmt.Errorf("parse jwt aud claim: %w", err)
	}
	if v.audience != "" && !containsAudience(audiences, v.audience) {
		return RIDClaims{}, fmt.Errorf("jwt audience mismatch: expected %q", v.audience)
	}

	return RIDClaims{
		Subject:  payload.Subject,
		Issuer:   payload.Issuer,
		Audience: audiences,
		Scope:    payload.Scope,
		ClientID: payload.ClientID,
		TokenUse: payload.TokenUse,
		KeyID:    hdr.Kid,
		Expires:  expUnix,
	}, nil
}

func (v *RIDVerifier) resolveKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if cached := v.lookupCachedKey(kid); cached != nil {
		return cached, nil
	}
	if err := v.refreshJWKS(ctx); err != nil {
		return nil, err
	}
	if refreshed := v.lookupCachedKey(kid); refreshed != nil {
		return refreshed, nil
	}
	return nil, fmt.Errorf("signing key not found in jwks for kid %q", kid)
}

func (v *RIDVerifier) lookupCachedKey(kid string) *rsa.PublicKey {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.fetchedAt.IsZero() || v.nowFunc().Sub(v.fetchedAt) > v.cacheTTL {
		return nil
	}
	if kid != "" {
		return v.keysByKID[kid]
	}
	if len(v.keysByKID) == 1 {
		for _, onlyKey := range v.keysByKID {
			return onlyKey
		}
	}
	return nil
}

func (v *RIDVerifier) refreshJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: unexpected status %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseBytes))
	if err != nil {
		return fmt.Errorf("read jwks response: %w", err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(bodyBytes, &doc); err != nil {
		return fmt.Errorf("parse jwks json: %w", err)
	}

	parsedKeys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, entry := range doc.Keys {
		if entry.Kty != "RSA" {
			continue
		}
		if entry.Use != "" && entry.Use != "sig" {
			continue
		}
		if entry.Alg != "" && entry.Alg != "RS256" {
			continue
		}
		pubKey, parseErr := jwkToRSAPublicKey(entry.N, entry.E)
		if parseErr != nil {
			continue
		}
		keyID := strings.TrimSpace(entry.Kid)
		parsedKeys[keyID] = pubKey
	}
	if len(parsedKeys) == 0 {
		return errors.New("jwks contains no usable RS256 RSA signing keys")
	}

	v.mu.Lock()
	v.keysByKID = parsedKeys
	v.fetchedAt = v.nowFunc()
	v.mu.Unlock()
	return nil
}

func jwkToRSAPublicKey(modulusB64, exponentB64 string) (*rsa.PublicKey, error) {
	modulusBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(modulusB64))
	if err != nil || len(modulusBytes) == 0 {
		return nil, errors.New("invalid jwk modulus")
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(exponentB64))
	if err != nil || len(exponentBytes) == 0 {
		return nil, errors.New("invalid jwk exponent")
	}

	modulus := new(big.Int).SetBytes(modulusBytes)
	exponentBig := new(big.Int).SetBytes(exponentBytes)
	if !exponentBig.IsInt64() {
		return nil, errors.New("jwk exponent overflow")
	}
	exponent := int(exponentBig.Int64())
	if exponent < 3 {
		return nil, errors.New("invalid jwk exponent")
	}
	return &rsa.PublicKey{N: modulus, E: exponent}, nil
}

func parseAudienceClaim(raw json.RawMessage) ([]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err == nil {
		return multiple, nil
	}
	return nil, errors.New("aud must be string or array of strings")
}

func containsAudience(audiences []string, expected string) bool {
	for _, candidate := range audiences {
		if candidate == expected {
			return true
		}
	}
	return false
}
