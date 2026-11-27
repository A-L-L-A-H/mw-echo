package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// AutoReplyFunc produces an automated reply for an incoming WhatsApp text message.
type AutoReplyFunc func(ctx context.Context, agentID, sender, text string) (string, error)

// ArchiveMediaFunc persists WhatsApp media bytes into the mw-echo storage pool and returns the object ID.
type ArchiveMediaFunc func(ctx context.Context, project, logicalPath, filename, mimeType string,
	content []byte) (string, error)

// Options configures authentication, default webhooks, and integration hooks for Manager.
type Options struct {
	BasicUser            string
	BasicPass            string
	DefaultWebhookURL    string
	DefaultWebhookSecret string
	AIGatewayURL         string
	AutoReply            AutoReplyFunc
	ArchiveMedia         ArchiveMediaFunc
	MediaUploadStream    MediaStreamUploadFunc
	MediaDownloadStream  MediaStreamDownloadFunc
}

// LoginResult contains live QR code assets and session state for device linking.
type LoginResult struct {
	DeviceID    string `json:"deviceId"`
	Status      string `json:"status"`
	Connected   bool   `json:"connected"`
	LoggedIn    bool   `json:"loggedIn"`
	JID         string `json:"jid,omitempty"`
	QR          string `json:"qr,omitempty"`
	QRLink      string `json:"qrLink"`
	QRDataURL   string `json:"qrDataUrl,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
}

// PairingResult contains the 8-digit phone link pairing code returned by WhatsApp.
type PairingResult struct {
	DeviceID    string `json:"deviceId"`
	Phone       string `json:"phone"`
	PairCode    string `json:"pair_code"`
	PairingCode string `json:"pairing_code"`
}

// SendResult identifies an outbound WhatsApp message acknowledged by the server.
type SendResult struct {
	MessageID string `json:"message_id"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

type cachedMedia struct {
	filename string
	mimeType string
	content  []byte
}

type downloadableRef struct {
	deviceID     string
	downloadable whatsmeow.DownloadableMessage
	filename     string
	mimeType     string
}

type deviceSession struct {
	mu          sync.RWMutex
	deviceID    string
	client      *whatsmeow.Client
	deviceStore *wastore.Device
	status      string
	qrCode      string
	qrPNG       []byte
	qrDataURL   string
	pairingCode string
	qrCancel    context.CancelFunc
	connected   bool
	loggedIn    bool
	lastError   string
}

// Manager orchestrates multi-device whatsmeow clients, SQLite persistence, webhooks, and GOWA routes.
type Manager struct {
	hub        *store.HubStore
	db         *sql.DB
	container  *sqlstore.Container
	opts       Options
	httpClient *http.Client

	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu            sync.RWMutex
	sessions      map[string]*deviceSession
	mediaCache    map[string]cachedMedia
	downloadables map[string]downloadableRef
	bgTasks       sync.WaitGroup
}

// NewManager opens the pure-Go modernc.org/sqlite whatsmeow session container and migrates HubStore WhatsApp tables.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("hub store is required")
	}
	if err := migrateTables(hub.DB()); err != nil {
		return nil, err
	}

	sessionsPath := filepath.Join(hub.DataDir(), "whatsapp_sessions.db")
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
		filepath.ToSlash(sessionsPath))
	container, err := sqlstore.New(context.Background(), "sqlite", dsn, waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sqlite store: %w", err)
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	if opts.AutoReply == nil && strings.TrimSpace(opts.AIGatewayURL) != "" {
		adapterFn, adapterErr := NewAIGatewayAutoReply(opts.AIGatewayURL, httpClient, "")
		if adapterErr != nil {
			return nil, fmt.Errorf("init ai gateway auto-reply: %w", adapterErr)
		}
		opts.AutoReply = adapterFn
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	mgr := &Manager{
		hub:           hub,
		db:            hub.DB(),
		container:     container,
		opts:          opts,
		httpClient:    httpClient,
		rootCtx:       rootCtx,
		rootCancel:    rootCancel,
		sessions:      make(map[string]*deviceSession),
		mediaCache:    make(map[string]cachedMedia),
		downloadables: make(map[string]downloadableRef),
	}
	return mgr, nil
}

// Close disconnects all active whatsmeow sessions, waits for async jobs, and closes the SQLite session store.
func (m *Manager) Close() error {
	m.rootCancel()

	m.mu.Lock()
	activeSessions := make([]*deviceSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		activeSessions = append(activeSessions, sess)
	}
	m.sessions = make(map[string]*deviceSession)
	m.mu.Unlock()

	for _, sess := range activeSessions {
		sess.mu.Lock()
		if sess.qrCancel != nil {
			sess.qrCancel()
			sess.qrCancel = nil
		}
		activeClient := sess.client
		sess.client = nil
		sess.connected = false
		sess.mu.Unlock()
		if activeClient != nil {
			activeClient.Disconnect()
		}
	}

	m.bgTasks.Wait()
	if m.container != nil {
		return m.container.Close()
	}
	return nil
}

// WaitBackground blocks until in-flight async webhook deliveries and auto-reply tasks finish.
func (m *Manager) WaitBackground() {
	m.bgTasks.Wait()
}

// EnsureDevice creates or retrieves a persisted device record and initializes its runtime session slot.
func (m *Manager) EnsureDevice(ctx context.Context, deviceID, label string) (DeviceRecord, error) {
	cleanID := normalizeDeviceID(deviceID)
	record, err := m.upsertDeviceRow(ctx, cleanID, label)
	if err != nil {
		return DeviceRecord{}, err
	}
	sess := m.getOrCreateSession(cleanID)
	return m.mergeSessionState(record, sess), nil
}

// ListDevices returns all persisted WhatsApp devices enriched with live session telemetry.
func (m *Manager) ListDevices(ctx context.Context) ([]DeviceRecord, error) {
	rows, err := m.listDeviceRows(ctx)
	if err != nil {
		return nil, err
	}
	enriched := make([]DeviceRecord, 0, len(rows))
	for _, row := range rows {
		sess := m.findSession(row.DeviceID)
		enriched = append(enriched, m.mergeSessionState(row, sess))
	}
	return enriched, nil
}

// GetDeviceStatus returns a single device's configuration and live QR/connection state.
func (m *Manager) GetDeviceStatus(ctx context.Context, deviceID string) (DeviceRecord, error) {
	cleanID := normalizeDeviceID(deviceID)
	row, err := m.getDeviceRow(ctx, cleanID)
	if err != nil {
		return DeviceRecord{}, err
	}
	sess := m.findSession(cleanID)
	return m.mergeSessionState(row, sess), nil
}

// UpdateDeviceConfig patches label, webhook settings, AI auto-reply, or pool archiving for a device.
func (m *Manager) UpdateDeviceConfig(ctx context.Context, deviceID string, patch DeviceConfigPatch) (DeviceRecord,
	error) {
	updated, err := m.updateDeviceConfigRow(ctx, deviceID, patch)
	if err != nil {
		return DeviceRecord{}, err
	}
	sess := m.findSession(updated.DeviceID)
	return m.mergeSessionState(updated, sess), nil
}

// Stats returns aggregate WhatsApp device, connection, chat, and message counts.
func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	return m.queryStats(ctx)
}

// ListChats lists persisted conversations filtered by optional device ID and search query.
func (m *Manager) ListChats(ctx context.Context, deviceID, searchQuery string, limit int) ([]ChatRecord, error) {
	return m.listChatRows(ctx, deviceID, searchQuery, limit)
}

// ListMessages lists persisted messages for a chat on a device.
func (m *Manager) ListMessages(ctx context.Context, deviceID, chatJID string, limit int) ([]MessageRecord, error) {
	return m.listMessageRows(ctx, deviceID, chatJID, limit)
}

// GetQRPNG retrieves the latest generated QR PNG bytes for a device session.
func (m *Manager) GetQRPNG(deviceID string) ([]byte, bool) {
	sess := m.findSession(normalizeDeviceID(deviceID))
	if sess == nil {
		return nil, false
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if len(sess.qrPNG) == 0 {
		return nil, false
	}
	copied := make([]byte, len(sess.qrPNG))
	copy(copied, sess.qrPNG)
	return copied, true
}

// ConnectQR connects a device session, starting a whatsmeow QR login channel when not yet paired.
func (m *Manager) ConnectQR(ctx context.Context, deviceID string) (LoginResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	record, err := m.EnsureDevice(ctx, cleanID, "")
	if err != nil {
		return LoginResult{}, err
	}
	sess, client, err := m.ensureClient(ctx, cleanID, record.JID)
	if err != nil {
		return LoginResult{}, err
	}

	if client.Store.ID != nil {
		if !client.IsConnected() {
			if err := client.Connect(); err != nil {
				return LoginResult{}, fmt.Errorf("connect paired device %q: %w", cleanID, err)
			}
		}
		sess.mu.Lock()
		sess.connected = client.IsConnected()
		sess.loggedIn = client.IsLoggedIn()
		sess.status = "connected"
		sess.qrCode = ""
		sess.qrPNG = nil
		sess.qrDataURL = ""
		sess.pairingCode = ""
		sess.mu.Unlock()
		_ = m.updateDeviceStatusAndJID(ctx, cleanID, "connected", client.Store.ID.String(), false)
		return m.buildLoginResult(cleanID, sess, client.Store.ID.String()), nil
	}

	sess.mu.Lock()
	if sess.qrCancel != nil {
		sess.qrCancel()
		sess.qrCancel = nil
	}
	qrCtx, qrCancel := context.WithCancel(m.rootCtx)
	sess.qrCancel = qrCancel
	sess.status = "connecting"
	sess.lastError = ""
	sess.mu.Unlock()

	if client.IsConnected() {
		client.Disconnect()
	}

	qrChan, err := client.GetQRChannel(qrCtx)
	if err != nil {
		qrCancel()
		return LoginResult{}, fmt.Errorf("open qr channel: %w", err)
	}

	firstReady := make(chan struct{}, 1)
	m.bgTasks.Add(1)
	go func() {
		defer m.bgTasks.Done()
		m.consumeQRChannel(cleanID, sess, qrChan, firstReady)
	}()

	if err := client.Connect(); err != nil {
		qrCancel()
		return LoginResult{}, fmt.Errorf("connect whatsapp websocket: %w", err)
	}

	waitTimer := time.NewTimer(5 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-firstReady:
	case <-ctx.Done():
	case <-waitTimer.C:
	}

	return m.buildLoginResult(cleanID, sess, ""), nil
}

// RequestPairingCode connects an unpaired device and requests an 8-digit phone link pairing code.
func (m *Manager) RequestPairingCode(ctx context.Context, deviceID, phone string) (PairingResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	digitsOnly := stripNonDigits(phone)
	if digitsOnly == "" {
		return PairingResult{}, errors.New("phone number is required")
	}

	record, err := m.EnsureDevice(ctx, cleanID, "")
	if err != nil {
		return PairingResult{}, err
	}
	sess, client, err := m.ensureClient(ctx, cleanID, record.JID)
	if err != nil {
		return PairingResult{}, err
	}
	if client.Store.ID != nil {
		return PairingResult{}, errors.New("device is already logged in; logout before pairing")
	}

	if !client.IsConnected() {
		sess.mu.Lock()
		if sess.qrCancel != nil {
			sess.qrCancel()
		}
		qrCtx, qrCancel := context.WithCancel(m.rootCtx)
		sess.qrCancel = qrCancel
		sess.status = "connecting"
		sess.mu.Unlock()

		qrChan, qrErr := client.GetQRChannel(qrCtx)
		if qrErr != nil {
			qrCancel()
			return PairingResult{}, fmt.Errorf("prepare pairing qr channel: %w", qrErr)
		}
		firstReady := make(chan struct{}, 1)
		m.bgTasks.Add(1)
		go func() {
			defer m.bgTasks.Done()
			m.consumeQRChannel(cleanID, sess, qrChan, firstReady)
		}()
		if err := client.Connect(); err != nil {
			qrCancel()
			return PairingResult{}, fmt.Errorf("connect for phone pairing: %w", err)
		}
		waitTimer := time.NewTimer(3 * time.Second)
		select {
		case <-firstReady:
		case <-ctx.Done():
			waitTimer.Stop()
			return PairingResult{}, ctx.Err()
		case <-waitTimer.C:
		}
		waitTimer.Stop()
	}

	pairCode, err := client.PairPhone(ctx, digitsOnly, true, whatsmeow.PairClientChrome, "Chrome (Windows)")
	if err != nil {
		return PairingResult{}, fmt.Errorf("request phone pairing code: %w", err)
	}

	sess.mu.Lock()
	sess.pairingCode = pairCode
	sess.status = "pairing"
	sess.mu.Unlock()
	_ = m.updateDeviceStatusAndJID(ctx, cleanID, "pairing", "", false)

	return PairingResult{
		DeviceID:    cleanID,
		Phone:       digitsOnly,
		PairCode:    pairCode,
		PairingCode: pairCode,
	}, nil
}

// Logout unlinks the WhatsApp device session, disconnects its socket, and clears local credentials.
func (m *Manager) Logout(ctx context.Context, deviceID string) error {
	cleanID := normalizeDeviceID(deviceID)
	row, err := m.getDeviceRow(ctx, cleanID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	sess := m.findSession(cleanID)
	if sess != nil {
		sess.mu.Lock()
		if sess.qrCancel != nil {
			sess.qrCancel()
			sess.qrCancel = nil
		}
		activeClient := sess.client
		devStore := sess.deviceStore
		sess.client = nil
		sess.deviceStore = nil
		sess.qrCode = ""
		sess.qrPNG = nil
		sess.qrDataURL = ""
		sess.pairingCode = ""
		sess.connected = false
		sess.loggedIn = false
		sess.status = "logged_out"
		sess.mu.Unlock()

		if activeClient != nil {
			if activeClient.Store != nil && activeClient.Store.ID != nil {
				if logoutErr := activeClient.Logout(ctx); logoutErr != nil {
					activeClient.Disconnect()
					_ = m.container.DeleteDevice(ctx, activeClient.Store)
				}
			} else {
				activeClient.Disconnect()
			}
		} else if devStore != nil && devStore.ID != nil {
			_ = m.container.DeleteDevice(ctx, devStore)
		}
	}

	if row.JID != "" {
		if parsedJID, parseErr := types.ParseJID(row.JID); parseErr == nil {
			if storedDev, lookupErr := m.container.GetDevice(ctx, parsedJID); lookupErr == nil && storedDev != nil {
				_ = m.container.DeleteDevice(ctx, storedDev)
			}
		}
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return m.updateDeviceStatusAndJID(ctx, cleanID, "logged_out", "", true)
	}
	return nil
}

// Reconnect restarts the WhatsApp connection for a paired device, or initiates QR login if unpaired.
func (m *Manager) Reconnect(ctx context.Context, deviceID string) (DeviceRecord, error) {
	cleanID := normalizeDeviceID(deviceID)
	if _, err := m.EnsureDevice(ctx, cleanID, ""); err != nil {
		return DeviceRecord{}, err
	}

	sess := m.getOrCreateSession(cleanID)
	sess.mu.Lock()
	if sess.qrCancel != nil {
		sess.qrCancel()
		sess.qrCancel = nil
	}
	prevClient := sess.client
	sess.client = nil
	sess.connected = false
	sess.mu.Unlock()
	if prevClient != nil {
		prevClient.Disconnect()
	}

	if _, err := m.ConnectQR(ctx, cleanID); err != nil {
		return DeviceRecord{}, err
	}
	return m.GetDeviceStatus(ctx, cleanID)
}

// DeleteDevice unlinks and removes a device along with its chats and message history.
func (m *Manager) DeleteDevice(ctx context.Context, deviceID string) error {
	cleanID := normalizeDeviceID(deviceID)
	if _, err := m.getDeviceRow(ctx, cleanID); err != nil {
		return err
	}
	_ = m.Logout(ctx, cleanID)

	m.mu.Lock()
	delete(m.sessions, cleanID)
	m.mu.Unlock()

	return m.deleteDeviceRows(ctx, cleanID)
}

// SendText sends a plain or reply/mention text message from a connected device.
func (m *Manager) SendText(ctx context.Context, deviceID, phone, message, replyToID string,
	mentions []string) (SendResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	trimmedMsg := strings.TrimSpace(message)
	if trimmedMsg == "" {
		return SendResult{}, errors.New("message text is required")
	}
	targetJID, err := parseTargetJID(phone)
	if err != nil {
		return SendResult{}, err
	}

	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return SendResult{}, err
	}

	var waMsg *waE2E.Message
	if strings.TrimSpace(replyToID) == "" && len(mentions) == 0 {
		waMsg = &waE2E.Message{Conversation: proto.String(trimmedMsg)}
	} else {
		waMsg = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(trimmedMsg),
				ContextInfo: buildContextInfo(replyToID, targetJID.String(), mentions),
			},
		}
	}

	sendResp, err := client.SendMessage(ctx, targetJID, waMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send text message: %w", err)
	}

	sentAt := sendResp.Timestamp.UTC()
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	tsFormatted := sentAt.Format(time.RFC3339)
	msgID := string(sendResp.ID)
	senderJID := ""
	if client.Store != nil && client.Store.ID != nil {
		senderJID = client.Store.ID.ToNonAD().String()
	}

	_ = m.upsertChatRow(ctx, ChatRecord{
		DeviceID:        cleanID,
		JID:             targetJID.ToNonAD().String(),
		LastMessage:     trimmedMsg,
		LastMessageTime: tsFormatted,
	}, false)
	_ = m.upsertMessageRow(ctx, MessageRecord{
		ID:        msgID,
		DeviceID:  cleanID,
		ChatJID:   targetJID.ToNonAD().String(),
		SenderJID: senderJID,
		FromMe:    true,
		MsgType:   "text",
		Body:      trimmedMsg,
		Status:    "sent",
		Timestamp: tsFormatted,
	})

	return SendResult{
		MessageID: msgID,
		ID:        msgID,
		Timestamp: tsFormatted,
	}, nil
}

// SendMedia uploads and sends an image, video, audio, or document attachment.
func (m *Manager) SendMedia(ctx context.Context, deviceID, phone, kind, filename, mimeType string, mediaBytes []byte,
	caption string, ptt bool) (SendResult, error) {
	return m.SendMediaWithReply(ctx, deviceID, phone, kind, filename, mimeType, mediaBytes, caption, "", ptt)
}

// SendMediaWithReply uploads and sends a media attachment with optional quoted reply context.
func (m *Manager) SendMediaWithReply(ctx context.Context, deviceID, phone, kind, filename, mimeType string,
	mediaBytes []byte, caption, replyToID string, ptt bool) (SendResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	if len(mediaBytes) == 0 {
		return SendResult{}, errors.New("media payload is empty")
	}
	targetJID, err := parseTargetJID(phone)
	if err != nil {
		return SendResult{}, err
	}
	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return SendResult{}, err
	}

	normalizedKind := strings.ToLower(strings.TrimSpace(kind))
	if normalizedKind == "file" {
		normalizedKind = "document"
	}
	resolvedMime := strings.TrimSpace(mimeType)
	if resolvedMime == "" && filename != "" {
		resolvedMime = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	}
	if resolvedMime == "" {
		resolvedMime = http.DetectContentType(mediaBytes)
	}
	resolvedName := strings.TrimSpace(filename)
	if resolvedName == "" {
		resolvedName = normalizedKind + extFromMime(resolvedMime, ".bin")
	}

	var waMediaType whatsmeow.MediaType
	switch normalizedKind {
	case "image":
		waMediaType = whatsmeow.MediaImage
	case "video":
		waMediaType = whatsmeow.MediaVideo
	case "audio":
		waMediaType = whatsmeow.MediaAudio
	case "document":
		waMediaType = whatsmeow.MediaDocument
	default:
		return SendResult{}, fmt.Errorf("unsupported media kind %q", kind)
	}

	uploadResp, err := client.Upload(ctx, mediaBytes, waMediaType)
	if err != nil {
		return SendResult{}, fmt.Errorf("upload %s media: %w", normalizedKind, err)
	}

	var ctxInfo *waE2E.ContextInfo
	if strings.TrimSpace(replyToID) != "" {
		ctxInfo = buildContextInfo(replyToID, targetJID.String(), nil)
	}

	var waMsg *waE2E.Message
	switch normalizedKind {
	case "image":
		waMsg = &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:       proto.String(caption),
				Mimetype:      proto.String(resolvedMime),
				URL:           proto.String(uploadResp.URL),
				DirectPath:    proto.String(uploadResp.DirectPath),
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    proto.Uint64(uploadResp.FileLength),
				ContextInfo:   ctxInfo,
			},
		}
	case "video":
		waMsg = &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				Caption:       proto.String(caption),
				Mimetype:      proto.String(resolvedMime),
				URL:           proto.String(uploadResp.URL),
				DirectPath:    proto.String(uploadResp.DirectPath),
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    proto.Uint64(uploadResp.FileLength),
				ContextInfo:   ctxInfo,
			},
		}
	case "audio":
		waMsg = &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				Mimetype:      proto.String(resolvedMime),
				PTT:           proto.Bool(ptt),
				URL:           proto.String(uploadResp.URL),
				DirectPath:    proto.String(uploadResp.DirectPath),
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    proto.Uint64(uploadResp.FileLength),
				ContextInfo:   ctxInfo,
			},
		}
	case "document":
		waMsg = &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				Caption:       proto.String(caption),
				Title:         proto.String(resolvedName),
				FileName:      proto.String(resolvedName),
				Mimetype:      proto.String(resolvedMime),
				URL:           proto.String(uploadResp.URL),
				DirectPath:    proto.String(uploadResp.DirectPath),
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    proto.Uint64(uploadResp.FileLength),
				ContextInfo:   ctxInfo,
			},
		}
	}

	sendResp, err := client.SendMessage(ctx, targetJID, waMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send %s message: %w", normalizedKind, err)
	}

	sentAt := sendResp.Timestamp.UTC()
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	tsFormatted := sentAt.Format(time.RFC3339)
	msgID := string(sendResp.ID)
	m.cacheMedia(msgID, resolvedName, resolvedMime, mediaBytes)

	mediaObjectID := ""
	if deviceRow, rowErr := m.getDeviceRow(ctx, cleanID); rowErr == nil && deviceRow.ArchiveToPool {
		logicalPath := fmt.Sprintf("%s/%s/%s_%s", cleanID, sanitizePathSegment(targetJID.ToNonAD().String()), msgID,
			resolvedName)
		if m.opts.MediaUploadStream != nil {
			if archivedID, archErr := m.opts.MediaUploadStream(ctx, "whatsapp", logicalPath, resolvedName, resolvedMime,
				bytes.NewReader(mediaBytes)); archErr == nil {
				mediaObjectID = archivedID
			}
		} else if m.opts.ArchiveMedia != nil {
			if archivedID, archErr := m.opts.ArchiveMedia(ctx, "whatsapp", logicalPath, resolvedName, resolvedMime,
				mediaBytes); archErr == nil {
				mediaObjectID = archivedID
			}
		}
	}
	m.indexMessageMediaMetadata(ctx, cleanID, msgID, resolvedName, resolvedMime, mediaObjectID, mediaBytes)

	bodyPreview := strings.TrimSpace(caption)
	if bodyPreview == "" {
		bodyPreview = "[" + normalizedKind + "] " + resolvedName
	}
	senderJID := ""
	if client.Store != nil && client.Store.ID != nil {
		senderJID = client.Store.ID.ToNonAD().String()
	}

	_ = m.upsertChatRow(ctx, ChatRecord{
		DeviceID:        cleanID,
		JID:             targetJID.ToNonAD().String(),
		LastMessage:     bodyPreview,
		LastMessageTime: tsFormatted,
	}, false)
	_ = m.upsertMessageRow(ctx, MessageRecord{
		ID:            msgID,
		DeviceID:      cleanID,
		ChatJID:       targetJID.ToNonAD().String(),
		SenderJID:     senderJID,
		FromMe:        true,
		MsgType:       normalizedKind,
		Body:          bodyPreview,
		MediaObjectID: mediaObjectID,
		Status:        "sent",
		Timestamp:     tsFormatted,
	})

	return SendResult{
		MessageID: msgID,
		ID:        msgID,
		Timestamp: tsFormatted,
	}, nil
}

// SendEnvelope validates and dispatches a unified text, interactive, or media message envelope.
func (m *Manager) SendEnvelope(ctx context.Context, env SendMessageEnvelope) (SendResult, error) {
	if err := env.Validate(); err != nil {
		return SendResult{}, err
	}
	deviceID := normalizeDeviceID(firstNonEmpty(env.DeviceID, env.DeviceIDCamel, "default"))
	replyID := firstNonEmpty(env.ReplyMessageID, env.ReplyToID)
	kind := strings.ToLower(strings.TrimSpace(env.Type))

	switch kind {
	case "text":
		return m.SendText(ctx, deviceID, env.Phone, env.Message, replyID, env.Mentions)
	case "interactive":
		textBody := RenderInteractiveText(env.Interactive)
		if strings.TrimSpace(textBody) == "" {
			textBody = env.Message
		}
		return m.SendText(ctx, deviceID, env.Phone, textBody, replyID, env.Mentions)
	default:
		var mediaBytes []byte
		resolvedName := strings.TrimSpace(env.Filename)
		resolvedMime := strings.TrimSpace(env.MimeType)
		if strings.TrimSpace(env.MediaBase64) != "" {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.MediaBase64))
			if err != nil {
				return SendResult{}, fmt.Errorf("decode media_base64: %w", err)
			}
			mediaBytes = decoded
		} else {
			meta, body, err := m.StreamDownloadMedia(ctx, deviceID, env.MediaID)
			if err != nil {
				return SendResult{}, fmt.Errorf("resolve media_id %q: %w", env.MediaID, err)
			}
			raw, readErr := io.ReadAll(io.LimitReader(body, 64*1024*1024))
			_ = body.Close()
			if readErr != nil {
				return SendResult{}, readErr
			}
			mediaBytes = raw
			if resolvedName == "" {
				resolvedName = meta.Filename
			}
			if resolvedMime == "" {
				resolvedMime = meta.MimeType
			}
		}
		caption := firstNonEmpty(env.Caption, env.Message)
		return m.SendMediaWithReply(ctx, deviceID, env.Phone, kind, resolvedName, resolvedMime, mediaBytes, caption, replyID,
			env.PTT)
	}
}

// SendPresence broadcasts global availability or per-chat composing/recording state.
func (m *Manager) SendPresence(ctx context.Context, deviceID, phone, state, media string) error {
	client, err := m.requireConnectedClient(normalizeDeviceID(deviceID))
	if err != nil {
		return err
	}
	trimmedPhone := strings.TrimSpace(phone)
	normalizedState := strings.ToLower(strings.TrimSpace(state))

	if trimmedPhone == "" {
		globalPresence := types.PresenceAvailable
		if normalizedState == "unavailable" || normalizedState == "offline" {
			globalPresence = types.PresenceUnavailable
		}
		return client.SendPresence(ctx, globalPresence)
	}

	targetJID, err := parseTargetJID(trimmedPhone)
	if err != nil {
		return err
	}
	chatPresence := types.ChatPresenceComposing
	if normalizedState == "paused" || normalizedState == "stop" {
		chatPresence = types.ChatPresencePaused
	}
	chatMedia := types.ChatPresenceMediaText
	normalizedMedia := strings.ToLower(strings.TrimSpace(media))
	if normalizedMedia == "audio" || normalizedMedia == "recording" || normalizedState == "recording" {
		chatMedia = types.ChatPresenceMediaAudio
	}
	return client.SendChatPresence(ctx, targetJID, chatPresence, chatMedia)
}

// MarkRead sends a read receipt for a message and zeroes its chat's local unread counter.
func (m *Manager) MarkRead(ctx context.Context, deviceID, messageID, chatJID, senderJID string) error {
	cleanID := normalizeDeviceID(deviceID)
	resolvedChat := strings.TrimSpace(chatJID)
	resolvedSender := strings.TrimSpace(senderJID)

	if resolvedChat == "" {
		if storedMsg, err := m.getMessageRow(ctx, messageID); err == nil {
			resolvedChat = storedMsg.ChatJID
			if resolvedSender == "" {
				resolvedSender = storedMsg.SenderJID
			}
		}
	}
	if resolvedChat == "" {
		return errors.New("chat jid or known message id is required")
	}

	targetChat, err := parseTargetJID(resolvedChat)
	if err != nil {
		return err
	}
	var parsedSender types.JID
	if resolvedSender != "" {
		if sJID, sErr := parseTargetJID(resolvedSender); sErr == nil {
			parsedSender = sJID
		}
	}

	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return err
	}
	if err := client.MarkRead(ctx, []types.MessageID{types.MessageID(messageID)}, time.Now().UTC(), targetChat,
		parsedSender); err != nil {
		return err
	}
	_ = m.updateMessageRow(ctx, messageID, "", "read", false)
	_ = m.resetChatUnreadRow(ctx, cleanID, targetChat.ToNonAD().String())
	return nil
}

// SendReaction sends an emoji reaction targeting an existing message.
func (m *Manager) SendReaction(ctx context.Context, deviceID, messageID, phone, emoji string) (SendResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	targetChat, senderJID, err := m.resolveMessageTargets(ctx, messageID, phone)
	if err != nil {
		return SendResult{}, err
	}
	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return SendResult{}, err
	}

	reactionMsg := client.BuildReaction(targetChat, senderJID, types.MessageID(messageID), emoji)
	sendResp, err := client.SendMessage(ctx, targetChat, reactionMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send reaction: %w", err)
	}
	return SendResult{
		MessageID: string(sendResp.ID),
		ID:        string(sendResp.ID),
		Timestamp: sendResp.Timestamp.UTC().Format(time.RFC3339),
	}, nil
}

// EditMessage updates the text of an already sent message.
func (m *Manager) EditMessage(ctx context.Context, deviceID, messageID, phone, newMessage string) (SendResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	trimmedNew := strings.TrimSpace(newMessage)
	if trimmedNew == "" {
		return SendResult{}, errors.New("updated message text is required")
	}
	targetChat, _, err := m.resolveMessageTargets(ctx, messageID, phone)
	if err != nil {
		return SendResult{}, err
	}
	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return SendResult{}, err
	}

	editMsg := client.BuildEdit(targetChat, types.MessageID(messageID),
		&waE2E.Message{Conversation: proto.String(trimmedNew)})
	sendResp, err := client.SendMessage(ctx, targetChat, editMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("edit message: %w", err)
	}
	_ = m.updateMessageRow(ctx, messageID, trimmedNew, "edited", true)
	return SendResult{
		MessageID: string(sendResp.ID),
		ID:        string(sendResp.ID),
		Timestamp: sendResp.Timestamp.UTC().Format(time.RFC3339),
	}, nil
}

// RevokeMessage deletes a previously sent message for everyone in the chat.
func (m *Manager) RevokeMessage(ctx context.Context, deviceID, messageID, phone string) (SendResult, error) {
	cleanID := normalizeDeviceID(deviceID)
	targetChat, _, err := m.resolveMessageTargets(ctx, messageID, phone)
	if err != nil {
		return SendResult{}, err
	}
	client, err := m.requireConnectedClient(cleanID)
	if err != nil {
		return SendResult{}, err
	}

	revokeMsg := client.BuildRevoke(targetChat, types.EmptyJID, types.MessageID(messageID))
	sendResp, err := client.SendMessage(ctx, targetChat, revokeMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("revoke message: %w", err)
	}
	_ = m.updateMessageRow(ctx, messageID, "", "revoked", false)
	return SendResult{
		MessageID: string(sendResp.ID),
		ID:        string(sendResp.ID),
		Timestamp: sendResp.Timestamp.UTC().Format(time.RFC3339),
	}, nil
}

// DownloadMessageMedia retrieves media bytes from local memory cache, WhatsApp servers, or HubStore blob storage.
func (m *Manager) DownloadMessageMedia(ctx context.Context, deviceID, messageID string) (string, string, []byte,
	error) {
	trimmedID := strings.TrimSpace(messageID)
	if trimmedID == "" {
		return "", "", nil, errors.New("message id is required")
	}

	m.mu.RLock()
	cached, hasCached := m.mediaCache[trimmedID]
	dlRef, hasDL := m.downloadables[trimmedID]
	m.mu.RUnlock()

	if hasCached {
		copied := make([]byte, len(cached.content))
		copy(copied, cached.content)
		return cached.filename, cached.mimeType, copied, nil
	}

	if hasDL {
		targetDevice := dlRef.deviceID
		if targetDevice == "" {
			targetDevice = normalizeDeviceID(deviceID)
		}
		if client, err := m.requireConnectedClient(targetDevice); err == nil {
			downloaded, dlErr := client.Download(ctx, dlRef.downloadable)
			if dlErr == nil && len(downloaded) > 0 {
				m.cacheMedia(trimmedID, dlRef.filename, dlRef.mimeType, downloaded)
				return dlRef.filename, dlRef.mimeType, downloaded, nil
			}
		}
	}

	storedMsg, err := m.getMessageRow(ctx, trimmedID)
	if err != nil {
		return "", "", nil, err
	}
	if storedMsg.MediaObjectID != "" {
		blobObj, diskPath, blobErr := m.hub.GetBlob(ctx, storedMsg.MediaObjectID)
		if blobErr == nil && diskPath != "" {
			fileBytes, readErr := os.ReadFile(diskPath)
			if readErr == nil {
				return blobObj.Filename, blobObj.MimeType, fileBytes, nil
			}
		}
	}
	return "", "", nil, sql.ErrNoRows
}

// HandleMessageEvent processes an incoming whatsmeow message event through persistence, media archiving, webhook dispatch, and AI auto-reply.
func (m *Manager) HandleMessageEvent(ctx context.Context, deviceID string, evt *events.Message) error {
	cleanID := normalizeDeviceID(deviceID)
	var activeClient *whatsmeow.Client
	if sess := m.findSession(cleanID); sess != nil {
		sess.mu.RLock()
		activeClient = sess.client
		sess.mu.RUnlock()
	}
	return m.processIncomingMessage(ctx, cleanID, activeClient, evt)
}

func (m *Manager) handleClientEvent(deviceID string, client *whatsmeow.Client, rawEvent any) {
	switch typedEvt := rawEvent.(type) {
	case *events.PairSuccess:
		jidStr := typedEvt.ID.String()
		if sess := m.findSession(deviceID); sess != nil {
			sess.mu.Lock()
			sess.status = "connected"
			sess.connected = true
			sess.loggedIn = true
			sess.qrCode = ""
			sess.qrPNG = nil
			sess.qrDataURL = ""
			sess.pairingCode = ""
			sess.mu.Unlock()
		}
		_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "connected", jidStr, false)

	case *events.Connected:
		jidStr := ""
		if client.Store != nil && client.Store.ID != nil {
			jidStr = client.Store.ID.String()
		}
		if sess := m.findSession(deviceID); sess != nil {
			sess.mu.Lock()
			sess.status = "connected"
			sess.connected = true
			sess.loggedIn = client.IsLoggedIn()
			sess.qrCode = ""
			sess.qrPNG = nil
			sess.qrDataURL = ""
			sess.pairingCode = ""
			sess.mu.Unlock()
		}
		_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "connected", jidStr, false)
		_ = client.SendPresence(m.rootCtx, types.PresenceAvailable)

	case *events.Disconnected:
		if sess := m.findSession(deviceID); sess != nil {
			sess.mu.Lock()
			sess.connected = false
			if sess.status == "connected" {
				sess.status = "disconnected"
			}
			sess.mu.Unlock()
		}
		_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "disconnected", "", false)

	case *events.LoggedOut:
		if sess := m.findSession(deviceID); sess != nil {
			sess.mu.Lock()
			sess.connected = false
			sess.loggedIn = false
			sess.status = "logged_out"
			sess.qrCode = ""
			sess.qrPNG = nil
			sess.qrDataURL = ""
			sess.pairingCode = ""
			sess.mu.Unlock()
		}
		_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "logged_out", "", true)

	case *events.Message:
		_ = m.processIncomingMessage(m.rootCtx, deviceID, client, typedEvt)
	}
}

func (m *Manager) processIncomingMessage(ctx context.Context, deviceID string, client *whatsmeow.Client,
	evt *events.Message) error {
	if evt == nil {
		return errors.New("message event is nil")
	}
	cleanID := normalizeDeviceID(deviceID)
	deviceRow, err := m.getDeviceRow(ctx, cleanID)
	if errors.Is(err, sql.ErrNoRows) {
		deviceRow, err = m.upsertDeviceRow(ctx, cleanID, "")
	}
	if err != nil {
		return err
	}

	msgID := strings.TrimSpace(string(evt.Info.ID))
	if msgID == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", cleanID, evt.Info.Chat.String(), time.Now().UTC().UnixNano())))
		msgID = "wa_" + hex.EncodeToString(sum[:8])
	}
	chatJID := evt.Info.Chat.ToNonAD().String()
	senderJID := evt.Info.Sender.ToNonAD().String()
	fromMe := evt.Info.IsFromMe
	pushName := strings.TrimSpace(evt.Info.PushName)
	msgTime := evt.Info.Timestamp.UTC()
	if msgTime.IsZero() {
		msgTime = time.Now().UTC()
	}
	tsFormatted := msgTime.Format(time.RFC3339)

	extracted := extractMessageContent(msgID, evt.Message)
	mediaObjectID := ""

	if extracted.downloadable != nil {
		m.rememberDownloadable(cleanID, msgID, extracted.downloadable, extracted.filename, extracted.mimeType)
		if client != nil && deviceRow.ArchiveToPool {
			if downloadedBytes, dlErr := client.Download(ctx, extracted.downloadable); dlErr == nil && len(downloadedBytes) > 0 {
				m.cacheMedia(msgID, extracted.filename, extracted.mimeType, downloadedBytes)
				logicalPath := fmt.Sprintf("%s/%s/%s_%s", cleanID, sanitizePathSegment(chatJID), msgID, extracted.filename)
				if m.opts.MediaUploadStream != nil {
					if archivedID, archErr := m.opts.MediaUploadStream(ctx, "whatsapp", logicalPath, extracted.filename,
						extracted.mimeType, bytes.NewReader(downloadedBytes)); archErr == nil {
						mediaObjectID = archivedID
					}
				} else if m.opts.ArchiveMedia != nil {
					if archivedID, archErr := m.opts.ArchiveMedia(ctx, "whatsapp", logicalPath, extracted.filename, extracted.mimeType,
						downloadedBytes); archErr == nil {
						mediaObjectID = archivedID
					}
				}
				m.indexMessageMediaMetadata(ctx, cleanID, msgID, extracted.filename, extracted.mimeType, mediaObjectID,
					downloadedBytes)
			}
		}
	}

	chatPreview := strings.TrimSpace(extracted.body)
	if chatPreview == "" {
		chatPreview = "[" + extracted.msgType + "]"
	}
	chatName := ""
	if !fromMe && pushName != "" {
		chatName = pushName
	}

	if err := m.upsertChatRow(ctx, ChatRecord{
		DeviceID:        cleanID,
		JID:             chatJID,
		Name:            chatName,
		LastMessage:     chatPreview,
		LastMessageTime: tsFormatted,
	}, !fromMe); err != nil {
		return err
	}

	msgStatus := "received"
	if fromMe {
		msgStatus = "sent"
	}
	record := MessageRecord{
		ID:            msgID,
		DeviceID:      cleanID,
		ChatJID:       chatJID,
		SenderJID:     senderJID,
		FromMe:        fromMe,
		MsgType:       extracted.msgType,
		Body:          extracted.body,
		MediaObjectID: mediaObjectID,
		Status:        msgStatus,
		Timestamp:     tsFormatted,
	}
	if err := m.upsertMessageRow(ctx, record); err != nil {
		return err
	}

	webhookURL := strings.TrimSpace(deviceRow.WebhookURL)
	if webhookURL == "" {
		webhookURL = strings.TrimSpace(m.opts.DefaultWebhookURL)
	}
	webhookSecret := strings.TrimSpace(deviceRow.WebhookSecret)
	if webhookSecret == "" {
		webhookSecret = strings.TrimSpace(m.opts.DefaultWebhookSecret)
	}
	if webhookURL != "" {
		webhookBody := map[string]any{
			"event":     "message",
			"device_id": cleanID,
			"payload": map[string]any{
				"id":              record.ID,
				"chat_id":         record.ChatJID,
				"from":            record.SenderJID,
				"from_me":         record.FromMe,
				"push_name":       pushName,
				"type":            record.MsgType,
				"body":            record.Body,
				"media_object_id": record.MediaObjectID,
				"timestamp":       record.Timestamp,
			},
		}
		m.bgTasks.Add(1)
		go func(targetURL, signingSecret string, eventBody map[string]any) {
			defer m.bgTasks.Done()
			m.dispatchWebhook(targetURL, signingSecret, cleanID, eventBody)
		}(webhookURL, webhookSecret, webhookBody)
	}

	if deviceRow.AIAutoReply && !fromMe && strings.TrimSpace(extracted.body) != "" && m.opts.AutoReply != nil {
		agentID := strings.TrimSpace(deviceRow.AIAgentID)
		if agentID == "" {
			agentID = "default"
		}
		inboundText := extracted.body
		m.bgTasks.Add(1)
		go func() {
			defer m.bgTasks.Done()
			replyCtx, cancel := context.WithTimeout(m.rootCtx, 60*time.Second)
			defer cancel()
			replyText, replyErr := m.opts.AutoReply(replyCtx, agentID, senderJID, inboundText)
			if replyErr != nil || strings.TrimSpace(replyText) == "" {
				return
			}
			_, _ = m.SendText(replyCtx, cleanID, chatJID, replyText, msgID, nil)
		}()
	}

	return nil
}

func (m *Manager) dispatchWebhook(targetURL, signingSecret, deviceID string, eventBody map[string]any) {
	encodedBody, err := json.Marshal(eventBody)
	if err != nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(m.rootCtx, 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(encodedBody))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Device-Id", deviceID)
	httpReq.Header.Set("User-Agent", "mw-echo-whatsapp/1.0")
	if signingSecret != "" {
		httpReq.Header.Set("X-Hub-Signature-256", SignWebhookPayload([]byte(signingSecret), encodedBody))
	}

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return
	}
	defer httpResp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(httpResp.Body, 4096))
}

func (m *Manager) consumeQRChannel(deviceID string, sess *deviceSession, qrChan <-chan whatsmeow.QRChannelItem,
	firstReady chan<- struct{}) {
	signaled := false
	signalOnce := func() {
		if !signaled {
			signaled = true
			select {
			case firstReady <- struct{}{}:
			default:
			}
		}
	}
	defer signalOnce()

	for qrEvt := range qrChan {
		switch qrEvt.Event {
		case whatsmeow.QRChannelEventCode:
			if err := m.applySessionQR(sess, qrEvt.Code); err == nil {
				_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "qr_ready", "", false)
			}
			signalOnce()
		case whatsmeow.QRChannelSuccess.Event:
			sess.mu.Lock()
			sess.qrCode = ""
			sess.qrPNG = nil
			sess.qrDataURL = ""
			sess.pairingCode = ""
			sess.status = "connected"
			sess.connected = true
			sess.loggedIn = true
			sess.mu.Unlock()
			_ = m.updateDeviceStatusAndJID(m.rootCtx, deviceID, "connected", "", false)
			signalOnce()
		default:
			sess.mu.Lock()
			if qrEvt.Error != nil {
				sess.lastError = qrEvt.Error.Error()
			}
			if !sess.connected {
				sess.status = "disconnected"
			}
			sess.mu.Unlock()
			signalOnce()
		}
	}
}

func (m *Manager) applySessionQR(sess *deviceSession, rawQR string) error {
	pngBytes, dataURL, err := encodeQRCode(rawQR)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sess.qrCode = rawQR
	sess.qrPNG = pngBytes
	sess.qrDataURL = dataURL
	sess.status = "qr_ready"
	sess.mu.Unlock()
	return nil
}

func encodeQRCode(rawQR string) ([]byte, string, error) {
	pngBytes, err := GenerateQRPNG(rawQR)
	if err != nil {
		return nil, "", err
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	return pngBytes, dataURL, nil
}

func (m *Manager) getOrCreateSession(deviceID string) *deviceSession {
	cleanID := normalizeDeviceID(deviceID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[cleanID]; ok {
		return existing
	}
	created := &deviceSession{
		deviceID: cleanID,
		status:   "disconnected",
	}
	m.sessions[cleanID] = created
	return created
}

func (m *Manager) findSession(deviceID string) *deviceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[normalizeDeviceID(deviceID)]
}

func (m *Manager) ensureClient(ctx context.Context, deviceID, storedJID string) (*deviceSession, *whatsmeow.Client,
	error) {
	sess := m.getOrCreateSession(deviceID)
	sess.mu.Lock()
	if sess.client != nil {
		existing := sess.client
		sess.mu.Unlock()
		return sess, existing, nil
	}
	sess.mu.Unlock()

	var devStore *wastore.Device
	if strings.TrimSpace(storedJID) != "" {
		if parsedJID, err := types.ParseJID(storedJID); err == nil {
			if found, lookupErr := m.container.GetDevice(ctx, parsedJID); lookupErr == nil && found != nil {
				devStore = found
			}
		}
	}
	if devStore == nil {
		devStore = m.container.NewDevice()
	}

	client := whatsmeow.NewClient(devStore, waLog.Noop)
	client.AddEventHandler(func(rawEvent any) {
		m.handleClientEvent(deviceID, client, rawEvent)
	})

	sess.mu.Lock()
	if sess.client != nil {
		existing := sess.client
		sess.mu.Unlock()
		return sess, existing, nil
	}
	sess.deviceStore = devStore
	sess.client = client
	sess.mu.Unlock()
	return sess, client, nil
}

func (m *Manager) requireConnectedClient(deviceID string) (*whatsmeow.Client, error) {
	sess := m.findSession(deviceID)
	if sess == nil {
		return nil, fmt.Errorf("device %q is not connected", deviceID)
	}
	sess.mu.RLock()
	activeClient := sess.client
	sess.mu.RUnlock()
	if activeClient == nil || !activeClient.IsConnected() || !activeClient.IsLoggedIn() {
		return nil, fmt.Errorf("device %q is not connected and logged in", deviceID)
	}
	return activeClient, nil
}

func (m *Manager) mergeSessionState(record DeviceRecord, sess *deviceSession) DeviceRecord {
	record.ID = record.DeviceID
	if sess == nil {
		return record
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	if sess.client != nil {
		record.Connected = sess.client.IsConnected()
		record.LoggedIn = sess.client.IsLoggedIn()
		if sess.client.Store != nil && sess.client.Store.ID != nil {
			record.JID = sess.client.Store.ID.String()
		}
	} else {
		record.Connected = sess.connected
		record.LoggedIn = sess.loggedIn
	}
	if sess.status != "" && (sess.status != "disconnected" || record.Status == "") {
		record.Status = sess.status
	}
	if record.Connected && record.LoggedIn {
		record.Status = "connected"
	}
	if sess.qrCode != "" {
		record.QR = sess.qrCode
		record.QRLink = "/statics/qr/" + record.DeviceID + ".png"
		record.QRDataURL = sess.qrDataURL
	}
	if sess.pairingCode != "" {
		record.PairingCode = sess.pairingCode
	}
	return record
}

func (m *Manager) buildLoginResult(deviceID string, sess *deviceSession, jidStr string) LoginResult {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	resolvedJID := jidStr
	if resolvedJID == "" && sess.client != nil && sess.client.Store != nil && sess.client.Store.ID != nil {
		resolvedJID = sess.client.Store.ID.String()
	}
	return LoginResult{
		DeviceID:    deviceID,
		Status:      sess.status,
		Connected:   sess.connected || (sess.client != nil && sess.client.IsConnected()),
		LoggedIn:    sess.loggedIn || (sess.client != nil && sess.client.IsLoggedIn()),
		JID:         resolvedJID,
		QR:          sess.qrCode,
		QRLink:      "/statics/qr/" + deviceID + ".png",
		QRDataURL:   sess.qrDataURL,
		PairingCode: sess.pairingCode,
	}
}

func (m *Manager) resolveMessageTargets(ctx context.Context, messageID, phone string) (types.JID, types.JID, error) {
	var chatJID types.JID
	var senderJID types.JID

	if strings.TrimSpace(phone) != "" {
		parsed, err := parseTargetJID(phone)
		if err != nil {
			return types.EmptyJID, types.EmptyJID, err
		}
		chatJID = parsed
	}
	if storedMsg, err := m.getMessageRow(ctx, messageID); err == nil {
		if chatJID.IsEmpty() && storedMsg.ChatJID != "" {
			if parsed, parseErr := parseTargetJID(storedMsg.ChatJID); parseErr == nil {
				chatJID = parsed
			}
		}
		if storedMsg.SenderJID != "" && chatJID.Server == types.GroupServer {
			if parsedSender, sErr := parseTargetJID(storedMsg.SenderJID); sErr == nil {
				senderJID = parsedSender
			}
		}
	}
	if chatJID.IsEmpty() {
		return types.EmptyJID, types.EmptyJID, errors.New("phone or known message id is required")
	}
	return chatJID, senderJID, nil
}

func (m *Manager) cacheMedia(messageID, filename, mimeType string, content []byte) {
	if len(content) == 0 {
		return
	}
	copied := make([]byte, len(content))
	copy(copied, content)
	m.mu.Lock()
	if len(m.mediaCache) >= 128 {
		for oldestKey := range m.mediaCache {
			delete(m.mediaCache, oldestKey)
			break
		}
	}
	m.mediaCache[messageID] = cachedMedia{
		filename: filename,
		mimeType: mimeType,
		content:  copied,
	}
	m.mu.Unlock()
}

func (m *Manager) rememberDownloadable(deviceID, messageID string, downloadable whatsmeow.DownloadableMessage, filename,
	mimeType string) {
	m.mu.Lock()
	if len(m.downloadables) >= 512 {
		for oldestKey := range m.downloadables {
			delete(m.downloadables, oldestKey)
			break
		}
	}
	m.downloadables[messageID] = downloadableRef{
		deviceID:     deviceID,
		downloadable: downloadable,
		filename:     filename,
		mimeType:     mimeType,
	}
	m.mu.Unlock()
}

type extractedMessage struct {
	msgType      string
	body         string
	downloadable whatsmeow.DownloadableMessage
	mimeType     string
	filename     string
}

func extractMessageContent(msgID string, msg *waE2E.Message) extractedMessage {
	if msg == nil {
		return extractedMessage{msgType: "unknown"}
	}
	if conv := msg.GetConversation(); conv != "" {
		return extractedMessage{msgType: "text", body: conv}
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return extractedMessage{msgType: "text", body: ext.GetText()}
	}
	if img := msg.GetImageMessage(); img != nil {
		mimeType := img.GetMimetype()
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return extractedMessage{
			msgType:      "image",
			body:         img.GetCaption(),
			downloadable: img,
			mimeType:     mimeType,
			filename:     msgID + extFromMime(mimeType, ".jpg"),
		}
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		mimeType := vid.GetMimetype()
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		return extractedMessage{
			msgType:      "video",
			body:         vid.GetCaption(),
			downloadable: vid,
			mimeType:     mimeType,
			filename:     msgID + extFromMime(mimeType, ".mp4"),
		}
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		mimeType := aud.GetMimetype()
		if mimeType == "" {
			mimeType = "audio/ogg"
		}
		return extractedMessage{
			msgType:      "audio",
			downloadable: aud,
			mimeType:     mimeType,
			filename:     msgID + extFromMime(mimeType, ".ogg"),
		}
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		mimeType := doc.GetMimetype()
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		fileName := strings.TrimSpace(doc.GetFileName())
		if fileName == "" {
			fileName = msgID + extFromMime(mimeType, ".bin")
		}
		bodyText := strings.TrimSpace(doc.GetCaption())
		if bodyText == "" {
			bodyText = fileName
		}
		return extractedMessage{
			msgType:      "document",
			body:         bodyText,
			downloadable: doc,
			mimeType:     mimeType,
			filename:     fileName,
		}
	}
	if stk := msg.GetStickerMessage(); stk != nil {
		mimeType := stk.GetMimetype()
		if mimeType == "" {
			mimeType = "image/webp"
		}
		return extractedMessage{
			msgType:      "sticker",
			downloadable: stk,
			mimeType:     mimeType,
			filename:     msgID + ".webp",
		}
	}
	if react := msg.GetReactionMessage(); react != nil {
		return extractedMessage{msgType: "reaction", body: react.GetText()}
	}
	return extractedMessage{msgType: "unknown"}
}

func buildContextInfo(replyToID, remoteJID string, mentions []string) *waE2E.ContextInfo {
	ctxInfo := &waE2E.ContextInfo{}
	if trimmedReply := strings.TrimSpace(replyToID); trimmedReply != "" {
		ctxInfo.StanzaID = proto.String(trimmedReply)
		if strings.TrimSpace(remoteJID) != "" {
			ctxInfo.RemoteJID = proto.String(remoteJID)
		}
	}
	if len(mentions) > 0 {
		normalizedMentions := make([]string, 0, len(mentions))
		for _, rawMention := range mentions {
			if jid, err := parseTargetJID(rawMention); err == nil {
				normalizedMentions = append(normalizedMentions, jid.String())
			}
		}
		if len(normalizedMentions) > 0 {
			ctxInfo.MentionedJID = normalizedMentions
		}
	}
	return ctxInfo
}

func parseTargetJID(raw string) (types.JID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return types.EmptyJID, errors.New("phone or jid is required")
	}
	if strings.Contains(trimmed, "@") {
		parsed, err := types.ParseJID(trimmed)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("invalid jid %q: %w", trimmed, err)
		}
		return parsed, nil
	}
	digits := stripNonDigits(trimmed)
	if digits == "" {
		return types.EmptyJID, fmt.Errorf("invalid phone number %q", trimmed)
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

func stripNonDigits(raw string) string {
	var builder strings.Builder
	for _, ch := range raw {
		if unicode.IsDigit(ch) {
			builder.WriteRune(ch)
		}
	}
	return builder.String()
}

func extFromMime(mimeType, fallback string) string {
	baseMime := strings.Split(mimeType, ";")[0]
	if exts, err := mime.ExtensionsByType(strings.TrimSpace(baseMime)); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return fallback
}

func sanitizePathSegment(raw string) string {
	replaced := strings.NewReplacer("/", "_", "\\", "_", "..", "_", ":", "_").Replace(strings.TrimSpace(raw))
	if replaced == "" {
		return "unknown"
	}
	return replaced
}
