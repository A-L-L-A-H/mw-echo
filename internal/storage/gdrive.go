package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultGoogleTokenURL   = "https://oauth2.googleapis.com/token"
	defaultGoogleAuthURL    = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleDriveAPI   = "https://www.googleapis.com/drive/v3"
	defaultGoogleUploadBase = "https://www.googleapis.com/upload/drive/v3"
)

type GDriveConfig struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RefreshToken string `json:"refreshToken"`
	RootFolderID string `json:"rootFolderId,omitempty"`
	RootFolder   string `json:"rootFolder,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	APIBase      string `json:"apiBase,omitempty"`
	UploadBase   string `json:"uploadBase,omitempty"`
}

type GDriveQuota struct {
	LimitBytes int64  `json:"limitBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	Email      string `json:"email,omitempty"`
}

type GDriveClient struct {
	cfg        GDriveConfig
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
	folderID    string
}

func NewGDriveClient(cfg GDriveConfig) *GDriveClient {
	if strings.TrimSpace(cfg.TokenURL) == "" {
		cfg.TokenURL = defaultGoogleTokenURL
	}
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = defaultGoogleDriveAPI
	}
	if strings.TrimSpace(cfg.UploadBase) == "" {
		cfg.UploadBase = defaultGoogleUploadBase
	}
	if strings.TrimSpace(cfg.RootFolder) == "" {
		cfg.RootFolder = "mw-echo"
	}
	return &GDriveClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		folderID:   strings.TrimSpace(cfg.RootFolderID),
	}
}

func BuildGoogleAuthURL(clientID, redirectURI, state string) string {
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"https://www.googleapis.com/auth/drive.file https://www.googleapis.com/auth/drive.metadata.readonly"},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {state},
	}
	return defaultGoogleAuthURL + "?" + params.Encode()
}

func ExchangeGoogleAuthCode(ctx context.Context, clientID, clientSecret, redirectURI, code, tokenURL string) (string, error) {
	if strings.TrimSpace(tokenURL) == "" {
		tokenURL = defaultGoogleTokenURL
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"code":          {code},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google oauth token exchange HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.RefreshToken == "" {
		return "", errors.New("google oauth response did not include refresh_token (ensure prompt=consent)")
	}
	return parsed.RefreshToken, nil
}

func (g *GDriveClient) getAccessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	if g.accessToken != "" && time.Now().Before(g.expiresAt.Add(-30*time.Second)) {
		tok := g.accessToken
		g.mu.Unlock()
		return tok, nil
	}
	g.mu.Unlock()

	if strings.TrimSpace(g.cfg.ClientID) == "" || strings.TrimSpace(g.cfg.ClientSecret) == "" || strings.TrimSpace(g.cfg.RefreshToken) == "" {
		return "", errors.New("google drive clientId, clientSecret, and refreshToken are required")
	}

	form := url.Values{
		"client_id":     {g.cfg.ClientID},
		"client_secret": {g.cfg.ClientSecret},
		"refresh_token": {g.cfg.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("google token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google token refresh HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil || tokResp.AccessToken == "" {
		return "", errors.New("invalid google access_token response")
	}
	if tokResp.ExpiresIn <= 0 {
		tokResp.ExpiresIn = 3500
	}

	g.mu.Lock()
	g.accessToken = tokResp.AccessToken
	g.expiresAt = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	g.mu.Unlock()
	return tokResp.AccessToken, nil
}

func (g *GDriveClient) ensureRootFolder(ctx context.Context, token string) (string, error) {
	g.mu.Lock()
	if g.folderID != "" {
		id := g.folderID
		g.mu.Unlock()
		return id, nil
	}
	g.mu.Unlock()

	folderName := strings.ReplaceAll(g.cfg.RootFolder, "'", "")
	q := url.QueryEscape(fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false", folderName))
	listURL := fmt.Sprintf("%s/files?q=%s&fields=files(id,name)&pageSize=1", strings.TrimRight(g.cfg.APIBase, "/"), q)
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return "", err
	}
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp, err := g.httpClient.Do(listReq)
	if err == nil {
		defer listResp.Body.Close()
		if listResp.StatusCode >= 200 && listResp.StatusCode < 300 {
			var found struct {
				Files []struct {
					ID string `json:"id"`
				} `json:"files"`
			}
			if json.NewDecoder(listResp.Body).Decode(&found) == nil && len(found.Files) > 0 && found.Files[0].ID != "" {
				g.mu.Lock()
				g.folderID = found.Files[0].ID
				g.mu.Unlock()
				return found.Files[0].ID, nil
			}
		}
	}

	metaBytes, _ := json.Marshal(map[string]any{
		"name":     folderName,
		"mimeType": "application/vnd.google-apps.folder",
	})
	createURL := strings.TrimRight(g.cfg.APIBase, "/") + "/files?fields=id"
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(metaBytes))
	if err != nil {
		return "", err
	}
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := g.httpClient.Do(createReq)
	if err != nil {
		return "", err
	}
	defer createResp.Body.Close()
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		return "", nil
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(createResp.Body).Decode(&created) == nil && created.ID != "" {
		g.mu.Lock()
		g.folderID = created.ID
		g.mu.Unlock()
		return created.ID, nil
	}
	return "", nil
}

func (g *GDriveClient) FetchQuota(ctx context.Context) (GDriveQuota, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return GDriveQuota{}, err
	}
	endpoint := strings.TrimRight(g.cfg.APIBase, "/") + "/about?fields=storageQuota,user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GDriveQuota{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return GDriveQuota{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GDriveQuota{}, fmt.Errorf("google drive quota HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		StorageQuota struct {
			Limit string `json:"limit"`
			Usage string `json:"usage"`
		} `json:"storageQuota"`
		User struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return GDriveQuota{}, err
	}
	limitBytes, _ := strconv.ParseInt(parsed.StorageQuota.Limit, 10, 64)
	usedBytes, _ := strconv.ParseInt(parsed.StorageQuota.Usage, 10, 64)
	if limitBytes <= 0 {
		limitBytes = 15 * 1024 * 1024 * 1024 // Default 15 GiB free Google account
	}
	return GDriveQuota{
		LimitBytes: limitBytes,
		UsedBytes:  usedBytes,
		Email:      parsed.User.EmailAddress,
	}, nil
}

func (g *GDriveClient) Upload(ctx context.Context, filename, mimeType string, content []byte) (string, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return "", err
	}
	folderID, _ := g.ensureRootFolder(ctx, token)
	meta := map[string]any{"name": filename}
	if folderID != "" {
		meta["parents"] = []string{folderID}
	}
	metaBytes, _ := json.Marshal(meta)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := mw.CreatePart(metaHeader)
	if err != nil {
		return "", err
	}
	_, _ = metaPart.Write(metaBytes)

	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	mediaHeader := make(textproto.MIMEHeader)
	mediaHeader.Set("Content-Type", mimeType)
	mediaPart, err := mw.CreatePart(mediaHeader)
	if err != nil {
		return "", err
	}
	_, _ = mediaPart.Write(content)
	_ = mw.Close()

	uploadURL := strings.TrimRight(g.cfg.UploadBase, "/") + "/files?uploadType=multipart&fields=id,size"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("google drive upload: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google drive upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBytes, &uploaded); err != nil || uploaded.ID == "" {
		return "", errors.New("google drive upload missing file id")
	}
	return uploaded.ID, nil
}

func (g *GDriveClient) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/files/%s?alt=media", strings.TrimRight(g.cfg.APIBase, "/"), url.PathEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("google drive download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (g *GDriveClient) Delete(ctx context.Context, fileID string) error {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/files/%s", strings.TrimRight(g.cfg.APIBase, "/"), url.PathEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("google drive delete HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
