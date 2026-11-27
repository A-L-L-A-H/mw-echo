package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type S3Config struct {
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	Bucket         string `json:"bucket"`
	AccessKey      string `json:"accessKey"`
	SecretKey      string `json:"secretKey"`
	ForcePathStyle bool   `json:"forcePathStyle"`
	Prefix         string `json:"prefix,omitempty"`
}

type S3Client struct {
	cfg        S3Config
	httpClient *http.Client
}

func NewS3Client(cfg S3Config) (*S3Client, error) {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Region = strings.TrimSpace(cfg.Region)
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3 endpoint, bucket, accessKey, and secretKey are required")
	}
	return &S3Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (c *S3Client) objectURL(key string) (string, string, error) {
	cleanKey := strings.TrimLeft(key, "/")
	if prefix := strings.Trim(c.cfg.Prefix, "/"); prefix != "" {
		cleanKey = prefix + "/" + cleanKey
	}
	encodedKey := encodeS3Path(cleanKey)
	parsed, err := url.Parse(c.cfg.Endpoint)
	if err != nil {
		return "", "", err
	}
	if c.cfg.ForcePathStyle || strings.Contains(parsed.Host, "127.0.0.1") || strings.Contains(parsed.Host, "localhost") {
		canonicalURI := "/" + url.PathEscape(c.cfg.Bucket) + "/" + encodedKey
		return c.cfg.Endpoint + canonicalURI, canonicalURI, nil
	}
	parsed.Host = c.cfg.Bucket + "." + parsed.Host
	canonicalURI := "/" + encodedKey
	return parsed.Scheme + "://" + parsed.Host + canonicalURI, canonicalURI, nil
}

func encodeS3Path(path string) string {
	parts := strings.Split(path, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

func (c *S3Client) signV4(req *http.Request, canonicalURI string, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadSum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadSum[:])

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Host", req.URL.Host)

	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", req.URL.Host, payloadHash,
		amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	reqSum := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hex.EncodeToString(reqSum[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, c.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

func (c *S3Client) PutObject(ctx context.Context, key, mimeType string, payload []byte) error {
	targetURL, canonicalURI, err := c.objectURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", mimeType)
	req.ContentLength = int64(len(payload))
	c.signV4(req, canonicalURI, payload)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *S3Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	targetURL, canonicalURI, err := c.objectURL(key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	c.signV4(req, canonicalURI, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("s3 get HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (c *S3Client) DeleteObject(ctx context.Context, key string) error {
	targetURL, canonicalURI, err := c.objectURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, targetURL, nil)
	if err != nil {
		return err
	}
	c.signV4(req, canonicalURI, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 delete HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *S3Client) HeadBucket(ctx context.Context) error {
	parsed, err := url.Parse(c.cfg.Endpoint)
	if err != nil {
		return err
	}
	canonicalURI := "/" + url.PathEscape(c.cfg.Bucket)
	targetURL := c.cfg.Endpoint + canonicalURI
	if !c.cfg.ForcePathStyle && !strings.Contains(parsed.Host, "127.0.0.1") && !strings.Contains(parsed.Host,
		"localhost") {
		canonicalURI = "/"
		targetURL = parsed.Scheme + "://" + c.cfg.Bucket + "." + parsed.Host + "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		return err
	}
	c.signV4(req, canonicalURI, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("s3 head bucket HTTP %d", resp.StatusCode)
	}
	return nil
}
