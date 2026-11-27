package whatsapp

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

//go:embed assets/*
var embeddedAssetsFS embed.FS

func (m *Manager) registerV1Routes(app *echo.Echo, api *echo.Group) {
	var v1 *echo.Group
	if api != nil {
		v1 = api.Group("/v1")
	} else if app != nil {
		v1 = app.Group("/api/v1", m.gowaBasicAuthMiddleware())
	} else {
		return
	}

	// System & Telemetry
	v1.GET("/system/health", m.handleV1SystemHealth)
	v1.GET("/system/metrics", m.handleV1SystemMetrics)

	// Auth & Session Lifecycle
	v1.POST("/auth/connect", m.handleV1AuthConnect)
	v1.GET("/auth/qr", m.handleV1AuthQR)
	v1.POST("/auth/pair", m.handleV1AuthPair)
	v1.POST("/auth/logout", m.handleV1AuthLogout)

	// Messaging & Streaming Media (9Drive zero-disk)
	v1.POST("/messages/send", m.handleV1SendMessage)
	v1.POST("/media/upload", m.handleV1MediaUpload)
	v1.GET("/media/download/:id", m.handleV1MediaDownload)

	// Inbox, Embedded Assets & Webhooks
	v1.GET("/inbox", m.handleV1Inbox)
	v1.GET("/assets/:filename", m.handleV1Asset)
	v1.POST("/webhooks/config", m.handleV1WebhookConfig)
}

func (m *Manager) handleV1SystemHealth(c echo.Context) error {
	stats, err := m.Stats(c.Request().Context())
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"status":            "ok",
		"engine":            stats.Engine,
		"devices_total":     stats.DevicesCount,
		"devices_connected": stats.ConnectedCount,
		"chats_total":       stats.ChatsCount,
		"messages_total":    stats.MessagesCount,
		"media_total":       stats.MediaCount,
	})
}

func (m *Manager) handleV1SystemMetrics(c echo.Context) error {
	stats, err := m.Stats(c.Request().Context())
	if err != nil {
		return c.String(http.StatusInternalServerError, "# error querying metrics\n")
	}
	var sb strings.Builder
	sb.WriteString("# HELP gowa_devices_total Total registered WhatsApp multi-device instances.\n")
	sb.WriteString("# TYPE gowa_devices_total gauge\n")
	fmt.Fprintf(&sb, "gowa_devices_total %d\n", stats.DevicesCount)
	sb.WriteString("# HELP gowa_devices_connected Currently connected WhatsApp multi-device instances.\n")
	sb.WriteString("# TYPE gowa_devices_connected gauge\n")
	fmt.Fprintf(&sb, "gowa_devices_connected %d\n", stats.ConnectedCount)
	sb.WriteString("# HELP gowa_chats_total Persisted WhatsApp chats.\n")
	sb.WriteString("# TYPE gowa_chats_total gauge\n")
	fmt.Fprintf(&sb, "gowa_chats_total %d\n", stats.ChatsCount)
	sb.WriteString("# HELP gowa_messages_total Persisted WhatsApp messages.\n")
	sb.WriteString("# TYPE gowa_messages_total counter\n")
	fmt.Fprintf(&sb, "gowa_messages_total %d\n", stats.MessagesCount)
	sb.WriteString("# HELP gowa_media_objects_total Indexed 9Drive-offloaded WhatsApp media objects.\n")
	sb.WriteString("# TYPE gowa_media_objects_total counter\n")
	fmt.Fprintf(&sb, "gowa_media_objects_total %d\n", stats.MediaCount)

	c.Response().Header().Set(echo.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
	return c.String(http.StatusOK, sb.String())
}

func (m *Manager) handleV1AuthConnect(c echo.Context) error {
	var body struct {
		DeviceID      string `json:"deviceId"`
		DeviceIDSnake string `json:"device_id"`
		Label         string `json:"label"`
	}
	_ = c.Bind(&body)
	deviceID := firstNonEmpty(body.DeviceID, body.DeviceIDSnake, resolveDeviceID(c))
	if strings.TrimSpace(body.Label) != "" {
		if _, err := m.EnsureDevice(c.Request().Context(), deviceID, body.Label); err != nil {
			return gowaError(c, http.StatusBadRequest, err.Error())
		}
	}
	loginState, err := m.ConnectQR(c.Request().Context(), deviceID)
	if err != nil {
		return gowaError(c, http.StatusBadGateway, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, loginState)
}

func (m *Manager) handleV1AuthQR(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	wantJSON := strings.EqualFold(strings.TrimSpace(c.QueryParam("format")), "json") ||
		(!strings.Contains(c.Request().Header.Get("Accept"), "image/png") &&
			strings.Contains(c.Request().Header.Get("Accept"), "application/json"))

	if wantJSON {
		record, err := m.GetDeviceStatus(c.Request().Context(), deviceID)
		if errors.Is(err, sql.ErrNoRows) {
			return gowaError(c, http.StatusNotFound, "device not found")
		}
		if err != nil {
			return gowaError(c, http.StatusInternalServerError, err.Error())
		}
		if record.QR == "" {
			return gowaError(c, http.StatusNotFound, "qr code not available for device")
		}
		return gowaSuccess(c, http.StatusOK, map[string]any{
			"device_id":   record.DeviceID,
			"status":      record.Status,
			"qr":          record.QR,
			"qr_link":     record.QRLink,
			"qr_data_url": record.QRDataURL,
		})
	}

	pngBytes, found := m.GetQRPNG(deviceID)
	if !found {
		return gowaError(c, http.StatusNotFound, "qr code not available for device")
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "image/png", pngBytes)
}

func (m *Manager) handleV1AuthPair(c echo.Context) error {
	var body struct {
		DeviceID      string `json:"deviceId"`
		DeviceIDSnake string `json:"device_id"`
		Phone         string `json:"phone"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid pair request JSON")
	}
	deviceID := firstNonEmpty(body.DeviceID, body.DeviceIDSnake, resolveDeviceID(c))
	phone := firstNonEmpty(body.Phone, c.QueryParam("phone"))
	if phone == "" {
		return gowaError(c, http.StatusBadRequest, "phone is required")
	}
	res, err := m.RequestPairingCode(c.Request().Context(), deviceID, phone)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, res)
}

func (m *Manager) handleV1AuthLogout(c echo.Context) error {
	var body struct {
		DeviceID      string `json:"deviceId"`
		DeviceIDSnake string `json:"device_id"`
	}
	_ = c.Bind(&body)
	deviceID := firstNonEmpty(body.DeviceID, body.DeviceIDSnake, resolveDeviceID(c))
	if err := m.Logout(c.Request().Context(), deviceID); err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"device_id": normalizeDeviceID(deviceID),
		"status":    "logged_out",
	})
}

func (m *Manager) handleV1SendMessage(c echo.Context) error {
	var env SendMessageEnvelope
	if err := c.Bind(&env); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid message envelope JSON")
	}
	if strings.TrimSpace(env.DeviceID) == "" && strings.TrimSpace(env.DeviceIDCamel) == "" {
		env.DeviceID = resolveDeviceID(c)
	}
	sent, err := m.SendEnvelope(c.Request().Context(), env)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, sent)
}

func (m *Manager) handleV1MediaUpload(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	contentType := c.Request().Header.Get("Content-Type")

	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		mr, err := c.Request().MultipartReader()
		if err != nil {
			return gowaError(c, http.StatusBadRequest, "invalid multipart stream")
		}
		var logicalPath string
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				return gowaError(c, http.StatusBadRequest, partErr.Error())
			}
			formName := part.FormName()
			if formName == "device_id" || formName == "deviceId" {
				rawVal, _ := io.ReadAll(io.LimitReader(part, 256))
				_ = part.Close()
				if trimmed := strings.TrimSpace(string(rawVal)); trimmed != "" {
					deviceID = normalizeDeviceID(trimmed)
				}
				continue
			}
			if formName == "logical_path" {
				rawVal, _ := io.ReadAll(io.LimitReader(part, 1024))
				_ = part.Close()
				logicalPath = strings.TrimSpace(string(rawVal))
				continue
			}
			if part.FileName() != "" || formName == "file" || formName == "media" {
				partMime := part.Header.Get("Content-Type")
				meta, upErr := m.StreamUploadMedia(c.Request().Context(), deviceID, logicalPath, part.FileName(), partMime, part)
				_ = part.Close()
				if upErr != nil {
					return gowaError(c, http.StatusBadRequest, upErr.Error())
				}
				return gowaSuccess(c, http.StatusCreated, meta)
			}
			_ = part.Close()
		}
		return gowaError(c, http.StatusBadRequest, "multipart file field is required")
	}

	filename := firstNonEmpty(c.Request().Header.Get("X-Filename"), c.QueryParam("filename"), "upload.bin")
	meta, err := m.StreamUploadMedia(c.Request().Context(), deviceID, c.QueryParam("logical_path"), filename, contentType,
		c.Request().Body)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusCreated, meta)
}

func (m *Manager) handleV1MediaDownload(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	mediaID := strings.TrimSpace(c.Param("id"))
	meta, body, err := m.StreamDownloadMedia(c.Request().Context(), deviceID, mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return gowaError(c, http.StatusNotFound, "media not found")
	}
	if err != nil {
		return gowaError(c, http.StatusBadGateway, err.Error())
	}
	defer func() { _ = body.Close() }()

	if meta.Filename != "" {
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(meta.Filename, `"`, "")))
	}
	if meta.SHA256 != "" {
		c.Response().Header().Set("X-Content-SHA256", meta.SHA256)
	}
	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return c.Stream(http.StatusOK, mimeType, body)
}

func (m *Manager) handleV1Inbox(c echo.Context) error {
	deviceID := firstNonEmpty(c.QueryParam("device_id"), c.QueryParam("deviceId"))
	chatJID := firstNonEmpty(c.QueryParam("chat_jid"), c.QueryParam("chatJid"))
	query := firstNonEmpty(c.QueryParam("q"), c.QueryParam("query"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	items, total, err := m.listInboxRows(c.Request().Context(), deviceID, chatJID, query, limit, offset)
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"messages": items,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
	})
}

func (m *Manager) handleV1Asset(c echo.Context) error {
	rawName := strings.TrimSpace(c.Param("filename"))
	cleaned := path.Clean("/" + rawName)
	base := path.Base(cleaned)
	if rawName == "" || base == "." || base == "/" || strings.Contains(rawName, "..") || strings.Contains(rawName,
		"/") || strings.Contains(rawName, "\\") {
		return gowaError(c, http.StatusBadRequest, "invalid asset filename")
	}
	content, err := embeddedAssetsFS.ReadFile("assets/" + base)
	if err != nil {
		return gowaError(c, http.StatusNotFound, "asset not found")
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(base)))
	if mimeType == "" {
		mimeType = http.DetectContentType(content)
	}
	return c.Blob(http.StatusOK, mimeType, content)
}

func (m *Manager) handleV1WebhookConfig(c echo.Context) error {
	var body struct {
		DeviceID           string  `json:"deviceId"`
		DeviceIDSnake      string  `json:"device_id"`
		URL                *string `json:"url"`
		Secret             *string `json:"secret"`
		WebhookURL         *string `json:"webhookUrl"`
		WebhookURLSnake    *string `json:"webhook_url"`
		WebhookSecret      *string `json:"webhookSecret"`
		WebhookSecretSnake *string `json:"webhook_secret"`
		AIAutoReply        *bool   `json:"aiAutoReply"`
		AIAutoReplySnake   *bool   `json:"ai_auto_reply"`
		AIAgentID          *string `json:"aiAgentId"`
		AIAgentIDSnake     *string `json:"ai_agent_id"`
		ArchiveToPool      *bool   `json:"archiveToPool"`
		ArchiveToPoolSnake *bool   `json:"archive_to_pool"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid webhook config JSON")
	}
	deviceID := firstNonEmpty(body.DeviceID, body.DeviceIDSnake, resolveDeviceID(c))
	var patch DeviceConfigPatch
	switch {
	case body.WebhookURLSnake != nil:
		patch.WebhookURL = body.WebhookURLSnake
	case body.WebhookURL != nil:
		patch.WebhookURL = body.WebhookURL
	case body.URL != nil:
		patch.WebhookURL = body.URL
	}
	switch {
	case body.WebhookSecretSnake != nil:
		patch.WebhookSecret = body.WebhookSecretSnake
	case body.WebhookSecret != nil:
		patch.WebhookSecret = body.WebhookSecret
	case body.Secret != nil:
		patch.WebhookSecret = body.Secret
	}
	if body.AIAutoReply != nil {
		patch.AIAutoReply = body.AIAutoReply
	} else if body.AIAutoReplySnake != nil {
		patch.AIAutoReply = body.AIAutoReplySnake
	}
	if body.AIAgentID != nil {
		patch.AIAgentID = body.AIAgentID
	} else if body.AIAgentIDSnake != nil {
		patch.AIAgentID = body.AIAgentIDSnake
	}
	if body.ArchiveToPool != nil {
		patch.ArchiveToPool = body.ArchiveToPool
	} else if body.ArchiveToPoolSnake != nil {
		patch.ArchiveToPool = body.ArchiveToPoolSnake
	}

	updated, err := m.UpdateDeviceConfig(c.Request().Context(), deviceID, patch)
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	res := formatGowaDevice(updated)
	res["has_webhook_secret"] = updated.HasWebhookSecret
	return gowaSuccess(c, http.StatusOK, res)
}
