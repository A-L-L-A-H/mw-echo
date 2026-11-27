package server_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/server"
)

const (
	contractIssuer   = "https://rid.moonwitness.local"
	contractAudience = "mw-echo"
	contractKID      = "kid-contract"
)

func encodeJWTUnsigned(t *testing.T, header, payload map[string]any) string {
	t.Helper()
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal unsigned jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal unsigned jwt payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes) + "."
}

func digestJWT(t *testing.T, hashAlg crypto.Hash, signingInput string) []byte {
	t.Helper()
	switch hashAlg {
	case crypto.SHA256:
		sum := sha256.Sum256([]byte(signingInput))
		return sum[:]
	case crypto.SHA384:
		sum := sha512.Sum384([]byte(signingInput))
		return sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512([]byte(signingInput))
		return sum[:]
	default:
		t.Fatalf("unsupported test signing hash %v", hashAlg)
		return nil
	}
}

func signJWTWithHash(t *testing.T, privateKey *rsa.PrivateKey, hashAlg crypto.Hash, header, payload map[string]any) string {
	t.Helper()
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, hashAlg, digestJWT(t, hashAlg, signingInput))
	if err != nil {
		t.Fatalf("sign jwt with %v: %v", hashAlg, err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func contractHeader(alg, kid string) map[string]any {
	return map[string]any{"alg": alg, "typ": "at+jwt", "kid": kid}
}

func validContractPayload(nowUnix int64) map[string]any {
	return map[string]any{
		"iss":       contractIssuer,
		"sub":       "contract-user",
		"aud":       []string{contractAudience},
		"exp":       nowUnix + 300,
		"nbf":       nowUnix - 10,
		"token_use": "access",
	}
}

func newContractVerifier(t *testing.T, jwksURL string, httpClient *http.Client) *server.RIDVerifier {
	t.Helper()
	verifier, err := server.NewRIDVerifier(server.RIDVerifierConfig{
		JWKSURL:    jwksURL,
		Issuer:     contractIssuer,
		Audience:   contractAudience,
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new contract rid verifier: %v", err)
	}
	return verifier
}

func assertRejected(t *testing.T, verifier *server.RIDVerifier, token, wantErr string) {
	t.Helper()
	_, err := verifier.VerifyAccessToken(context.Background(), token)
	if err == nil {
		t.Fatalf("expected token to be rejected with %q", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected error containing %q, got %q", wantErr, err.Error())
	}
}

func TestRIDVerifierRejectsForbiddenAlgorithms(t *testing.T) {
	signingKey := mustGenerateRSAKey(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{makeJWK(contractKID, &signingKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	verifier := newContractVerifier(t, jwksServer.URL, jwksServer.Client())
	nowUnix := time.Now().UTC().Unix()
	tests := []struct {
		name    string
		token   string
		wantErr string
	}{
		{
			name:    "alg none with empty signature",
			token:   encodeJWTUnsigned(t, contractHeader("none", contractKID), validContractPayload(nowUnix)),
			wantErr: `unsupported jwt alg "none"`,
		},
		{
			name:    "RS384 signed token",
			token:   signJWTWithHash(t, signingKey, crypto.SHA384, contractHeader("RS384", contractKID), validContractPayload(nowUnix)),
			wantErr: `unsupported jwt alg "RS384"`,
		},
		{
			name:    "RS512 signed token",
			token:   signJWTWithHash(t, signingKey, crypto.SHA512, contractHeader("RS512", contractKID), validContractPayload(nowUnix)),
			wantErr: `unsupported jwt alg "RS512"`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertRejected(t, verifier, testCase.token, testCase.wantErr)
		})
	}
}

func TestRIDVerifierRejectsWrongIssuerAndAudience(t *testing.T) {
	signingKey := mustGenerateRSAKey(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{makeJWK(contractKID, &signingKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	verifier := newContractVerifier(t, jwksServer.URL, jwksServer.Client())
	nowUnix := time.Now().UTC().Unix()

	wrongIssuerPayload := validContractPayload(nowUnix)
	wrongIssuerPayload["iss"] = "https://attacker.example"
	wrongAudiencePayload := validContractPayload(nowUnix)
	wrongAudiencePayload["aud"] = "other-service"

	tests := []struct {
		name    string
		payload map[string]any
		wantErr string
	}{
		{name: "wrong issuer", payload: wrongIssuerPayload, wantErr: "jwt issuer mismatch"},
		{name: "wrong audience", payload: wrongAudiencePayload, wantErr: "jwt audience mismatch"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			token := signJWTWithHash(t, signingKey, crypto.SHA256, contractHeader("RS256", contractKID), testCase.payload)
			assertRejected(t, verifier, token, testCase.wantErr)
		})
	}
}

func TestRIDVerifierRejectsBadSignature(t *testing.T) {
	trustedKey := mustGenerateRSAKey(t)
	attackerKey := mustGenerateRSAKey(t)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{makeJWK(contractKID, &trustedKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	verifier := newContractVerifier(t, jwksServer.URL, jwksServer.Client())
	nowUnix := time.Now().UTC().Unix()
	forgedToken := signJWTWithHash(t, attackerKey, crypto.SHA256, contractHeader("RS256", contractKID), validContractPayload(nowUnix))

	assertRejected(t, verifier, forgedToken, "invalid jwt signature")
}

func TestRIDVerifierRefreshesJWKSForUnknownKid(t *testing.T) {
	oldKey := mustGenerateRSAKey(t)
	newKey := mustGenerateRSAKey(t)
	rotated := atomic.Bool{}
	jwksFetches := atomic.Int32{}

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jwksFetches.Add(1)
		activeKey := &oldKey.PublicKey
		activeKID := "kid-old"
		if rotated.Load() {
			activeKey = &newKey.PublicKey
			activeKID = "kid-new"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{makeJWK(activeKID, activeKey)},
		})
	}))
	defer jwksServer.Close()

	verifier := newContractVerifier(t, jwksServer.URL, jwksServer.Client())
	nowUnix := time.Now().UTC().Unix()
	oldToken := signJWTWithHash(t, oldKey, crypto.SHA256, contractHeader("RS256", "kid-old"), validContractPayload(nowUnix))
	if _, err := verifier.VerifyAccessToken(context.Background(), oldToken); err != nil {
		t.Fatalf("expected initial kid-old token to pass: %v", err)
	}
	if jwksFetches.Load() < 1 {
		t.Fatalf("expected initial JWKS fetch, got %d", jwksFetches.Load())
	}

	rotated.Store(true)
	rotatedPayload := validContractPayload(nowUnix)
	rotatedPayload["sub"] = "rotated-user"
	rotatedToken := signJWTWithHash(t, newKey, crypto.SHA256, contractHeader("RS256", "kid-new"), rotatedPayload)
	claims, err := verifier.VerifyAccessToken(context.Background(), rotatedToken)
	if err != nil {
		t.Fatalf("expected unknown kid to trigger JWKS refresh: %v", err)
	}
	if claims.Subject != "rotated-user" || claims.KeyID != "kid-new" {
		t.Fatalf("unexpected rotated claims: %+v", claims)
	}
	if jwksFetches.Load() < 2 {
		t.Fatalf("expected JWKS refresh count >= 2, got %d", jwksFetches.Load())
	}
}
