package server_test

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/server"
)

func TestRS256JWKSVerifier(t *testing.T) {
	// Generate an RSA key pair for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	// Create RS256 JWT token manually (no external deps)
	header := map[string]any{
		"alg": "RS256",
		"typ": "JWT",
		"kid": "test-key-1",
	}
	payload := map[string]any{
		"iss":       "https://rid.example.com",
		"sub":       "user-123",
		"aud":       "mw-echo",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"nbf":       time.Now().Unix(),
		"scope":     "read:tasks",
		"client_id": "client-abc",
		"token_use": "access",
	}

	tokenString, err := signRS256Token(header, payload, privateKey)
	if err != nil {
		t.Fatalf("failed to sign RS256 token: %v", err)
	}

	// Create JWKS endpoint response first
	jwksResponse := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": "test-key-1",
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()),
			},
		},
	}

	// Set up mock JWKS server so we can reference its URL
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwksResponse)
	}))
	defer jwksServer.Close()

	// Create verifier config
	jwksURL := jwksServer.URL
	cfg := server.RIDVerifierConfig{
		JWKSURL:    jwksURL,
		Issuer:     "https://rid.example.com",
		Audience:   "mw-echo",
		CacheTTL:   5 * time.Minute,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Now:        time.Now,
	}

	verifier, err := server.NewRIDVerifier(cfg)
	if err != nil {
		t.Fatalf("failed to create RID verifier: %v", err)
	}

	// Test successful verification
	claimsResult, err := verifier.VerifyAccessToken(context.Background(), tokenString)
	if err != nil {
		t.Fatalf("unexpected verification error: %v", err)
	}

	if claimsResult.Issuer != "https://rid.example.com" {
		t.Errorf("expected issuer 'https://rid.example.com', got '%s'", claimsResult.Issuer)
	}
	if claimsResult.Subject != "user-123" {
		t.Errorf("expected subject 'user-123', got '%s'", claimsResult.Subject)
	}
	if !contains(claimsResult.Audience, "mw-echo") {
		t.Errorf("expected audience 'mw-echo'")
	}
	if claimsResult.Scope != "read:tasks" {
		t.Errorf("expected scope 'read:tasks', got '%s'", claimsResult.Scope)
	}
	if claimsResult.ClientID != "client-abc" {
		t.Errorf("expected client_id 'client-abc', got '%s'", claimsResult.ClientID)
	}
	if claimsResult.KeyID != "test-key-1" {
		t.Errorf("expected key_id 'test-key-1', got '%s'", claimsResult.KeyID)
	}

	t.Logf("RS256 verifier successful verification test passed")

	// Test HS256 rejection
	hs256Token := jwtManualSignHS256(map[string]any{
		"alg": "HS256",
		"typ": "JWT",
	}, map[string]any{
		"iss": "https://rid.example.com",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, []byte("secret"))

	_, err = verifier.VerifyAccessToken(context.Background(), hs256Token)
	if err == nil {
		t.Error("expected HS256 token to be rejected")
	}

	t.Logf("HS256 rejection test passed")

	// Test 'none' algorithm rejection
	noneToken := jwtManualSignNone(map[string]any{
		"alg": "none",
		"typ": "JWT",
	}, map[string]any{
		"iss": "https://rid.example.com",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	_, err = verifier.VerifyAccessToken(context.Background(), noneToken)
	if err == nil {
		t.Error("expected 'none' algorithm token to be rejected")
	}

	t.Logf("'none' algorithm rejection test passed")

	// Test ID token rejection
	idTokenPayload := map[string]any{
		"iss":       "https://rid.example.com",
		"sub":       "user-123",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"token_use": "id",
		"at_hash":   "some-hash",
		"c_hash":    "some-c-hash",
	}
	idToken, _ := signRS256Token(map[string]any{"alg": "RS256", "typ": "JWT"}, idTokenPayload, privateKey)

	_, err = verifier.VerifyAccessToken(context.Background(), idToken)
	if err == nil {
		t.Error("expected ID token to be rejected")
	}

	t.Logf("ID token rejection test passed")

	// Test audience mismatch
	audToken, _ := signRS256Token(map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss":   "https://rid.example.com",
		"sub":   "user-123",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"aud":   "wrong-audience",
		"scope": "read:tasks",
	}, privateKey)

	_, err = verifier.VerifyAccessToken(context.Background(), audToken)
	if err == nil {
		t.Error("expected audience mismatch to be rejected")
	}

	t.Logf("audience mismatch rejection test passed")

	// Test missing exp claim
	noExpToken, _ := signRS256Token(map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss": "https://rid.example.com",
		"sub": "user-123",
	}, privateKey)

	_, err = verifier.VerifyAccessToken(context.Background(), noExpToken)
	if err == nil {
		t.Error("expected missing exp to be rejected")
	}

	t.Logf("missing exp rejection test passed")

	// Test nbf not yet valid
	nbfFutureToken, _ := signRS256Token(map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss":   "https://rid.example.com",
		"sub":   "user-123",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"nbf":   time.Now().Add(24 * time.Hour).Unix(),
		"scope": "read:tasks",
	}, privateKey)

	_, err = verifier.VerifyAccessToken(context.Background(), nbfFutureToken)
	if err == nil {
		t.Error("expected nbf future rejection")
	}

	t.Logf("nbf future rejection test passed")

	t.Logf("All RS256 JWKS verifier tests passed!")
}

func signRS256Token(header, payload map[string]any, privKey *rsa.PrivateKey) (string, error) {
	headerBytes, _ := json.Marshal(header)
	payloadBytes, _ := json.Marshal(payload)

	headerEncoded := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerEncoded + "." + payloadEncoded

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	signatureEncoded := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + signatureEncoded, nil
}

func jwtManualSignHS256(header, payload map[string]any, secret []byte) string {
	headerBytes, _ := json.Marshal(header)
	payloadBytes, _ := json.Marshal(payload)
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerEncoded + "." + payloadEncoded

	// Simple HMAC256 signature for testing (will be rejected by RS256 verifier)
	mac := simpleHMACSHA256(secret, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func simpleHMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func jwtManualSignNone(header, payload map[string]any) string {
	headerBytes, _ := json.Marshal(header)
	payloadBytes, _ := json.Marshal(payload)
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerEncoded + "." + payloadEncoded
	return signingInput + "."
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
