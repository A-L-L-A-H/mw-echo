package server_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/server"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func mustGenerateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return priv
}

func makeJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func signRS256JWT(t *testing.T, priv *rsa.PrivateKey, header map[string]any, payload map[string]any) string {
	t.Helper()
	hdrBytes, _ := json.Marshal(header)
	payBytes, _ := json.Marshal(payload)
	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestRIDVerifierADR001AndKeyRotation(t *testing.T) {
	keyOne := mustGenerateRSAKey(t)
	keyTwo := mustGenerateRSAKey(t)

	var rotateToTwo atomic.Bool
	var jwksFetches atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksFetches.Add(1)
		keys := []map[string]string{makeJWK("kid-1", &keyOne.PublicKey)}
		if rotateToTwo.Load() {
			keys = append(keys, makeJWK("kid-2", &keyTwo.PublicKey))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	defer jwksServer.Close()

	verifier, err := server.NewRIDVerifier(server.RIDVerifierConfig{
		JWKSURL:    jwksServer.URL,
		Issuer:     "https://rid.moonwitness.local",
		Audience:   "mw-echo",
		HTTPClient: jwksServer.Client(),
	})
	if err != nil {
		t.Fatalf("new rid verifier: %v", err)
	}

	now := time.Now().UTC().Unix()
	validToken := signRS256JWT(t, keyOne,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{
			"iss":       "https://rid.moonwitness.local",
			"sub":       "user-42",
			"aud":       []string{"mw-echo", "mw-pocket"},
			"exp":       now + 300,
			"nbf":       now - 10,
			"token_use": "access",
		},
	)
	claims, err := verifier.VerifyAccessToken(context.Background(), validToken)
	if err != nil {
		t.Fatalf("expected valid RS256 token to pass, got: %v", err)
	}
	if claims.Subject != "user-42" || claims.KeyID != "kid-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	// Reject alg != RS256 (HS256 and none)
	hs256Token := signRS256JWT(t, keyOne,
		map[string]any{"alg": "HS256", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{"iss": "https://rid.moonwitness.local", "aud": "mw-echo", "exp": now + 300},
	)
	if _, err := verifier.VerifyAccessToken(context.Background(), hs256Token); err == nil {
		t.Fatalf("expected HS256 token to be rejected")
	}

	noneToken := signRS256JWT(t, keyOne,
		map[string]any{"alg": "none", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{"iss": "https://rid.moonwitness.local", "aud": "mw-echo", "exp": now + 300},
	)
	if _, err := verifier.VerifyAccessToken(context.Background(), noneToken); err == nil {
		t.Fatalf("expected alg=none token to be rejected")
	}

	// Reject token_use == "id"
	idToken := signRS256JWT(t, keyOne,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{"iss": "https://rid.moonwitness.local", "aud": "mw-echo", "exp": now + 300, "token_use": "id"},
	)
	if _, err := verifier.VerifyAccessToken(context.Background(), idToken); err == nil {
		t.Fatalf("expected OIDC id token to be rejected")
	}

	// Reject expired token
	expiredToken := signRS256JWT(t, keyOne,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{"iss": "https://rid.moonwitness.local", "aud": "mw-echo", "exp": now - 5},
	)
	if _, err := verifier.VerifyAccessToken(context.Background(), expiredToken); err == nil {
		t.Fatalf("expected expired token to be rejected")
	}

	// Reject future nbf
	futureNbfToken := signRS256JWT(t, keyOne,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-1"},
		map[string]any{"iss": "https://rid.moonwitness.local", "aud": "mw-echo", "exp": now + 300, "nbf": now + 120},
	)
	if _, err := verifier.VerifyAccessToken(context.Background(), futureNbfToken); err == nil {
		t.Fatalf("expected future nbf token to be rejected")
	}

	// Refresh JWKS automatically on unknown kid
	rotateToTwo.Store(true)
	rotatedToken := signRS256JWT(t, keyTwo,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-2"},
		map[string]any{"iss": "https://rid.moonwitness.local", "sub": "user-rotated", "aud": "mw-echo", "exp": now + 300},
	)
	rotatedClaims, err := verifier.VerifyAccessToken(context.Background(), rotatedToken)
	if err != nil {
		t.Fatalf("expected unknown kid to trigger JWKS refresh and succeed, got: %v", err)
	}
	if rotatedClaims.Subject != "user-rotated" || jwksFetches.Load() < 2 {
		t.Fatalf("expected JWKS refresh count >= 2, got %d (claims=%+v)", jwksFetches.Load(), rotatedClaims)
	}
}

func TestDualAuthMiddlewareAndPocketPairConfig(t *testing.T) {
	privKey := mustGenerateRSAKey(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{makeJWK("kid-auth", &privKey.PublicKey)}})
	}))
	defer jwksServer.Close()

	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	handler := server.NewEcho(hub, server.Config{
		Addr:          "127.0.0.1:4310",
		AuthToken:     "static-secret-token",
		RIDJWKSURL:    jwksServer.URL,
		RIDIssuer:     "https://rid.moonwitness.local",
		RIDAudience:   "mw-echo",
		RIDHTTPClient: jwksServer.Client(),
		EchoPublicURL: "https://echo.moonwitness.local",
		RIDURL:        "https://rid.moonwitness.local",
		MyLoveURL:     "https://mylove.moonwitness.local",
	})

	// Unauthenticated /api/stats rejected
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	// Static token accepted
	staticReq := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	staticReq.Header.Set("Authorization", "Bearer static-secret-token")
	staticRec := httptest.NewRecorder()
	handler.ServeHTTP(staticRec, staticReq)
	if staticRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with static token, got %d", staticRec.Code)
	}

	// Valid mw-rid RS256 Bearer JWT accepted
	now := time.Now().UTC().Unix()
	jwtToken := signRS256JWT(t, privKey,
		map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "kid-auth"},
		map[string]any{"iss": "https://rid.moonwitness.local", "sub": "mobile-user", "aud": "mw-echo", "exp": now + 300},
	)
	jwtReq := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	jwtReq.Header.Set("Authorization", "Bearer "+jwtToken)
	jwtRec := httptest.NewRecorder()
	handler.ServeHTTP(jwtRec, jwtReq)
	if jwtRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with mw-rid JWT, got %d: %s", jwtRec.Code, jwtRec.Body.String())
	}

	// GET /api/pocket/pair-config is accessible without exposing raw secret token
	pairRec := httptest.NewRecorder()
	handler.ServeHTTP(pairRec, httptest.NewRequest(http.MethodGet, "/api/pocket/pair-config", nil))
	if pairRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on pair-config, got %d: %s", pairRec.Code, pairRec.Body.String())
	}
	pairBody := pairRec.Body.String()
	if strings.Contains(pairBody, "static-secret-token") {
		t.Fatalf("pair-config must never expose raw static auth token: %s", pairBody)
	}
	if !strings.Contains(pairBody, `"pairUri":"mwpocket://pair?`) || !strings.Contains(pairBody, `"echoUrl":"https://echo.moonwitness.local"`) {
		t.Fatalf("unexpected pair-config payload: %s", pairBody)
	}
}
