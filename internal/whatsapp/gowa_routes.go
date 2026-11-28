package whatsapp

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type gowaEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Results any    `json:"results,omitempty"`
}

func gowaSuccess(c echo.Context, statusCode int, results any) error {
	return c.JSON(statusCode, gowaEnvelope{
		Code:    "SUCCESS",
		Message: "Success",
		Results: results,
	})
}

func gowaError(c echo.Context, statusCode int, message string) error {
	return c.JSON(statusCode, gowaEnvelope{
		Code:    "ERROR",
		Message: message,
	})
}

// RegisterRoutes mounts the drop-in GOWA REST contract on app and React UI management endpoints on api (/api/whatsapp/*).
func (m *Manager) RegisterRoutes(app *echo.Echo, api *echo.Group) {
	if app != nil {
		authGuard := m.gowaBasicAuthMiddleware()

		// Static QR PNG asset serving
		app.GET("/statics/qr/:file", m.handleServeQRPNG)

		// GOWA App & Multi-Device lifecycle
		app.GET("/app/info", m.handleGowaAppInfo, authGuard)
		app.GET("/app/devices", m.handleGowaListDevices, authGuard)
		app.GET("/devices", m.handleGowaListDevices, authGuard)
		app.POST("/devices", m.handleGowaCreateDevice, authGuard)
		app.GET("/devices/:id", m.handleGowaGetDevice, authGuard)
		app.GET("/devices/:id/status", m.handleGowaGetDevice, authGuard)
		app.DELETE("/devices/:id", m.handleGowaDeleteDevice, authGuard)

		app.GET("/app/login", m.handleGowaLoginQR, authGuard)
		app.GET("/devices/:id/login", m.handleGowaLoginQR, authGuard)
		app.GET("/app/login-with-code", m.handleGowaLoginWithCode, authGuard)

		app.GET("/app/logout", m.handleGowaLogout, authGuard)
		app.POST("/app/logout", m.handleGowaLogout, authGuard)
		app.POST("/devices/:id/logout", m.handleGowaLogout, authGuard)

		app.GET("/app/reconnect", m.handleGowaReconnect, authGuard)
		app.POST("/app/reconnect", m.handleGowaReconnect, authGuard)
		app.GET("/devices/:id/reconnect", m.handleGowaReconnect, authGuard)
		app.POST("/devices/:id/reconnect", m.handleGowaReconnect, authGuard)

		app.PATCH("/devices/:id/webhook", m.handleGowaUpdateWebhook, authGuard)

		// GOWA Messaging & Media
		app.POST("/send/message", m.handleGowaSendMessage, authGuard)
		app.POST("/send/image", m.handleGowaSendMedia("image"), authGuard)
		app.POST("/send/video", m.handleGowaSendMedia("video"), authGuard)
		app.POST("/send/audio", m.handleGowaSendMedia("audio"), authGuard)
		app.POST("/send/file", m.handleGowaSendMedia("file"), authGuard)
		app.POST("/send/presence", m.handleGowaSendPresence, authGuard)

		app.POST("/message/:id/read", m.handleGowaMarkRead, authGuard)
		app.POST("/message/:id/reaction", m.handleGowaReaction, authGuard)
		app.POST("/message/:id/update", m.handleGowaEditMessage, authGuard)
		app.POST("/message/:id/revoke", m.handleGowaRevokeMessage, authGuard)
		app.GET("/message/:id/download", m.handleGowaDownloadMedia, authGuard)

		// GOWA Chat & Message Queries
		app.GET("/chats", m.handleGowaListChats, authGuard)
		app.GET("/chat/:jid/messages", m.handleGowaListMessages, authGuard)
	}

	if api != nil {
		waGroup := api.Group("/whatsapp")
		waGroup.GET("/stats", m.handleUIStats)
		waGroup.GET("/devices", m.handleUIListDevices)
		waGroup.POST("/devices", m.handleUICreateDevice)
		waGroup.DELETE("/devices/:id", m.handleUIDeleteDevice)
		waGroup.POST("/devices/:id/login", m.handleUILoginQR)
		waGroup.POST("/devices/:id/pair", m.handleUIPairCode)
		waGroup.POST("/devices/:id/logout", m.handleUILogout)
		waGroup.POST("/devices/:id/reconnect", m.handleUIReconnect)
		waGroup.PATCH("/devices/:id/config", m.handleUIUpdateConfig)
		waGroup.GET("/chats", m.handleUIListChats)
		waGroup.GET("/chats/:jid/messages", m.handleUIListMessages)
		waGroup.POST("/send", m.handleUISendText)
	}

	m.registerV1Routes(app, api)
}

func (m *Manager) gowaBasicAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			expectedUser := m.opts.BasicUser
			expectedPass := m.opts.BasicPass
			if expectedUser == "" || expectedPass == "" {
				return next(c)
			}
			providedUser, providedPass, hasAuth := c.Request().BasicAuth()
			userMatch := subtle.ConstantTimeCompare([]byte(providedUser), []byte(expectedUser))
			passMatch := subtle.ConstantTimeCompare([]byte(providedPass), []byte(expectedPass))
			if !hasAuth || (userMatch&passMatch) != 1 {
				c.Response().Header().Set("WWW-Authenticate", `Basic realm="mw-echo-gowa"`)
				return gowaError(c, http.StatusUnauthorized, "unauthorized")
			}
			return next(c)
		}
	}
}

func resolveDeviceID(c echo.Context) string {
	if paramID := strings.TrimSpace(c.Param("id")); paramID != "" {
		return normalizeDeviceID(paramID)
	}
	if headerID := strings.TrimSpace(c.Request().Header.Get("X-Device-Id")); headerID != "" {
		return normalizeDeviceID(headerID)
	}
	if queryID := strings.TrimSpace(c.QueryParam("device_id")); queryID != "" {
		return normalizeDeviceID(queryID)
	}
	if queryCamel := strings.TrimSpace(c.QueryParam("deviceId")); queryCamel != "" {
		return normalizeDeviceID(queryCamel)
	}
	return "default"
}

func formatGowaDevice(record DeviceRecord) map[string]any {
	view := map[string]any{
		"id":              record.DeviceID,
		"device_id":       record.DeviceID,
		"deviceId":        record.DeviceID,
		"label":           record.Label,
		"jid":             record.JID,
		"status":          record.Status,
		"connected":       record.Connected,
		"logged_in":       record.LoggedIn,
		"loggedIn":        record.LoggedIn,
		"webhook_url":     record.WebhookURL,
		"ai_auto_reply":   record.AIAutoReply,
		"ai_agent_id":     record.AIAgentID,
		"archive_to_pool": record.ArchiveToPool,
		"created_at":      record.CreatedAt,
		"updated_at":      record.UpdatedAt,
	}
	if record.QR != "" {
		view["qr"] = record.QR
		view["qr_link"] = record.QRLink
		view["qr_data_url"] = record.QRDataURL
	}
	if record.PairingCode != "" {
		view["pairing_code"] = record.PairingCode
	}
	return view
}

// --- GOWA Handlers ---

func (m *Manager) handleServeQRPNG(c echo.Context) error {
	rawFile := strings.TrimSpace(c.Param("file"))
	deviceID := normalizeDeviceID(strings.TrimSuffix(rawFile, ".png"))
	pngBytes, found := m.GetQRPNG(deviceID)
	if !found {
		return gowaError(c, http.StatusNotFound, "qr code not available for device")
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "image/png", pngBytes)
}

func (m *Manager) handleGowaAppInfo(c echo.Context) error {
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"version": "mw-echo-gowa-v1",
		"engine":  "whatsmeow",
		"driver":  "gowa",
	})
}

func (m *Manager) handleGowaListDevices(c echo.Context) error {
	devices, err := m.ListDevices(c.Request().Context())
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	formatted := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		formatted = append(formatted, formatGowaDevice(device))
	}
	return gowaSuccess(c, http.StatusOK, formatted)
}

type createDeviceRequest struct {
	ID                 string `json:"id"`
	DeviceID           string `json:"deviceId"`
	DeviceIDSnake      string `json:"device_id"`
	Label              string `json:"label"`
	WebhookURL         string `json:"webhookUrl"`
	WebhookURLSnake    string `json:"webhook_url"`
	WebhookSecret      string `json:"webhookSecret"`
	WebhookSecretSnake string `json:"webhook_secret"`
	AIAutoReply        *bool  `json:"aiAutoReply"`
	AIAutoReplySnake   *bool  `json:"ai_auto_reply"`
	AIAgentID          string `json:"aiAgentId"`
	AIAgentIDSnake     string `json:"ai_agent_id"`
	ArchiveToPool      *bool  `json:"archiveToPool"`
	ArchiveToPoolSnake *bool  `json:"archive_to_pool"`
}

func (m *Manager) createDeviceFromRequest(c echo.Context) (DeviceRecord, error) {
	var body createDeviceRequest
	if err := c.Bind(&body); err != nil {
		return DeviceRecord{}, errors.New("invalid device JSON")
	}
	chosenID := firstNonEmpty(body.DeviceID, body.DeviceIDSnake, body.ID)
	if chosenID == "" {
		chosenID = "default"
	}
	record, err := m.EnsureDevice(c.Request().Context(), chosenID, body.Label)
	if err != nil {
		return DeviceRecord{}, err
	}

	var patch DeviceConfigPatch
	hasPatch := false
	if label := strings.TrimSpace(body.Label); label != "" {
		patch.Label = &label
		hasPatch = true
	}
	if webhookURL := firstNonEmpty(body.WebhookURL, body.WebhookURLSnake); webhookURL != "" {
		patch.WebhookURL = &webhookURL
		hasPatch = true
	}
	if webhookSecret := firstNonEmpty(body.WebhookSecret, body.WebhookSecretSnake); webhookSecret != "" {
		patch.WebhookSecret = &webhookSecret
		hasPatch = true
	}
	if body.AIAutoReply != nil {
		patch.AIAutoReply = body.AIAutoReply
		hasPatch = true
	} else if body.AIAutoReplySnake != nil {
		patch.AIAutoReply = body.AIAutoReplySnake
		hasPatch = true
	}
	if agentID := firstNonEmpty(body.AIAgentID, body.AIAgentIDSnake); agentID != "" {
		patch.AIAgentID = &agentID
		hasPatch = true
	}
	if body.ArchiveToPool != nil {
		patch.ArchiveToPool = body.ArchiveToPool
		hasPatch = true
	} else if body.ArchiveToPoolSnake != nil {
		patch.ArchiveToPool = body.ArchiveToPoolSnake
		hasPatch = true
	}
	if hasPatch {
		return m.UpdateDeviceConfig(c.Request().Context(), record.DeviceID, patch)
	}
	return record, nil
}

func (m *Manager) handleGowaCreateDevice(c echo.Context) error {
	record, err := m.createDeviceFromRequest(c)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusCreated, formatGowaDevice(record))
}

func (m *Manager) handleGowaGetDevice(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	record, err := m.GetDeviceStatus(c.Request().Context(), deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return gowaError(c, http.StatusNotFound, "device not found")
	}
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, formatGowaDevice(record))
}

func (m *Manager) handleGowaDeleteDevice(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	err := m.DeleteDevice(c.Request().Context(), deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return gowaError(c, http.StatusNotFound, "device not found")
	}
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{"device_id": deviceID, "deleted": true})
}

func (m *Manager) handleGowaLoginQR(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	loginState, err := m.ConnectQR(c.Request().Context(), deviceID)
	if err != nil {
		return gowaError(c, http.StatusBadGateway, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"device_id":    loginState.DeviceID,
		"status":       loginState.Status,
		"connected":    loginState.Connected,
		"logged_in":    loginState.LoggedIn,
		"jid":          loginState.JID,
		"qr":           loginState.QR,
		"qr_link":      loginState.QRLink,
		"qr_data_url":  loginState.QRDataURL,
		"pairing_code": loginState.PairingCode,
	})
}

func (m *Manager) handleGowaLoginWithCode(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	phone := strings.TrimSpace(c.QueryParam("phone"))
	if phone == "" {
		return gowaError(c, http.StatusBadRequest, "phone query parameter is required")
	}
	pairResult, err := m.RequestPairingCode(c.Request().Context(), deviceID, phone)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"device_id":    pairResult.DeviceID,
		"phone":        pairResult.Phone,
		"pair_code":    pairResult.PairCode,
		"pairing_code": pairResult.PairingCode,
	})
}

func (m *Manager) handleGowaLogout(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	if err := m.Logout(c.Request().Context(), deviceID); err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{
		"device_id": deviceID,
		"status":    "logged_out",
	})
}

func (m *Manager) handleGowaReconnect(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	record, err := m.Reconnect(c.Request().Context(), deviceID)
	if err != nil {
		return gowaError(c, http.StatusBadGateway, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, formatGowaDevice(record))
}

func (m *Manager) handleGowaUpdateWebhook(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	var body struct {
		URL                *string `json:"url"`
		Secret             *string `json:"secret"`
		WebhookURL         *string `json:"webhookUrl"`
		WebhookURLSnake    *string `json:"webhook_url"`
		WebhookSecret      *string `json:"webhookSecret"`
		WebhookSecretSnake *string `json:"webhook_secret"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid webhook JSON")
	}
	var patch DeviceConfigPatch
	if body.WebhookURLSnake != nil {
		patch.WebhookURL = body.WebhookURLSnake
	} else if body.WebhookURL != nil {
		patch.WebhookURL = body.WebhookURL
	} else if body.URL != nil {
		patch.WebhookURL = body.URL
	}
	if body.WebhookSecretSnake != nil {
		patch.WebhookSecret = body.WebhookSecretSnake
	} else if body.WebhookSecret != nil {
		patch.WebhookSecret = body.WebhookSecret
	} else if body.Secret != nil {
		patch.WebhookSecret = body.Secret
	}
	updated, err := m.UpdateDeviceConfig(c.Request().Context(), deviceID, patch)
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, formatGowaDevice(updated))
}

func (m *Manager) handleGowaSendMessage(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	var body struct {
		Phone          string   `json:"phone" form:"phone"`
		Message        string   `json:"message" form:"message"`
		ReplyMessageID string   `json:"reply_message_id" form:"reply_message_id"`
		ReplyToID      string   `json:"replyToId" form:"replyToId"`
		Mentions       []string `json:"mentions"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid send message payload")
	}
	replyID := firstNonEmpty(body.ReplyMessageID, body.ReplyToID)
	sent, err := m.SendText(c.Request().Context(), deviceID, body.Phone, body.Message, replyID, body.Mentions)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, sent)
}

func (m *Manager) handleGowaSendMedia(kind string) echo.HandlerFunc {
	return func(c echo.Context) error {
		deviceID := resolveDeviceID(c)
		phone := strings.TrimSpace(c.FormValue("phone"))
		caption := c.FormValue("caption")
		replyID := firstNonEmpty(c.FormValue("reply_message_id"), c.FormValue("replyToId"))
		ptt := parseBoolLoose(c.FormValue("ptt"))

		fileHeader, err := c.FormFile(kind)
		if err != nil {
			fileHeader, err = c.FormFile("file")
		}
		if err != nil {
			return gowaError(c, http.StatusBadRequest, fmt.Sprintf("multipart file field %q is required", kind))
		}
		srcFile, err := fileHeader.Open()
		if err != nil {
			return gowaError(c, http.StatusInternalServerError, err.Error())
		}
		defer srcFile.Close()

		mediaBytes, err := io.ReadAll(io.LimitReader(srcFile, 64*1024*1024))
		if err != nil {
			return gowaError(c, http.StatusBadRequest, err.Error())
		}

		sent, err := m.SendMediaWithReply(
			c.Request().Context(),
			deviceID,
			phone,
			kind,
			fileHeader.Filename,
			fileHeader.Header.Get("Content-Type"),
			mediaBytes,
			caption,
			replyID,
			ptt,
		)
		if err != nil {
			return gowaError(c, http.StatusBadRequest, err.Error())
		}
		return gowaSuccess(c, http.StatusOK, sent)
	}
}

func (m *Manager) handleGowaSendPresence(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	var body struct {
		Phone string `json:"phone"`
		Type  string `json:"type"`
		State string `json:"state"`
		Media string `json:"media"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid presence payload")
	}
	state := firstNonEmpty(body.State, body.Type)
	if err := m.SendPresence(c.Request().Context(), deviceID, body.Phone, state, body.Media); err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{"status": "sent"})
}

func (m *Manager) handleGowaMarkRead(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	messageID := strings.TrimSpace(c.Param("id"))
	var body struct {
		Phone     string `json:"phone"`
		ChatJID   string `json:"chat_jid"`
		SenderJID string `json:"sender_jid"`
	}
	_ = c.Bind(&body)
	targetChat := firstNonEmpty(body.ChatJID, body.Phone)
	if err := m.MarkRead(c.Request().Context(), deviceID, messageID, targetChat, body.SenderJID); err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{"message_id": messageID, "status": "read"})
}

func (m *Manager) handleGowaReaction(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	messageID := strings.TrimSpace(c.Param("id"))
	var body struct {
		Phone    string `json:"phone"`
		ChatJID  string `json:"chat_jid"`
		Emoji    string `json:"emoji"`
		Reaction string `json:"reaction"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid reaction payload")
	}
	targetChat := firstNonEmpty(body.ChatJID, body.Phone)
	emoji := firstNonEmpty(body.Emoji, body.Reaction)
	sent, err := m.SendReaction(c.Request().Context(), deviceID, messageID, targetChat, emoji)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, sent)
}

func (m *Manager) handleGowaEditMessage(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	messageID := strings.TrimSpace(c.Param("id"))
	var body struct {
		Phone   string `json:"phone"`
		ChatJID string `json:"chat_jid"`
		Message string `json:"message"`
	}
	if err := c.Bind(&body); err != nil {
		return gowaError(c, http.StatusBadRequest, "invalid edit payload")
	}
	targetChat := firstNonEmpty(body.ChatJID, body.Phone)
	sent, err := m.EditMessage(c.Request().Context(), deviceID, messageID, targetChat, body.Message)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, sent)
}

func (m *Manager) handleGowaRevokeMessage(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	messageID := strings.TrimSpace(c.Param("id"))
	var body struct {
		Phone   string `json:"phone"`
		ChatJID string `json:"chat_jid"`
	}
	_ = c.Bind(&body)
	targetChat := firstNonEmpty(body.ChatJID, body.Phone)
	sent, err := m.RevokeMessage(c.Request().Context(), deviceID, messageID, targetChat)
	if err != nil {
		return gowaError(c, http.StatusBadRequest, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, sent)
}

func (m *Manager) handleGowaDownloadMedia(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	messageID := strings.TrimSpace(c.Param("id"))
	filename, mimeType, content, err := m.DownloadMessageMedia(c.Request().Context(), deviceID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return gowaError(c, http.StatusNotFound, "media not found for message")
	}
	if err != nil {
		return gowaError(c, http.StatusBadGateway, err.Error())
	}
	if filename != "" {
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(filename, `"`, "")))
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return c.Blob(http.StatusOK, mimeType, content)
}

func (m *Manager) handleGowaListChats(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	searchQuery := firstNonEmpty(c.QueryParam("query"), c.QueryParam("q"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	chats, err := m.ListChats(c.Request().Context(), deviceID, searchQuery, limit)
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{"data": chats})
}

func (m *Manager) handleGowaListMessages(c echo.Context) error {
	deviceID := resolveDeviceID(c)
	rawJID := c.Param("jid")
	if decoded, err := url.PathUnescape(rawJID); err == nil {
		rawJID = decoded
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	messages, err := m.ListMessages(c.Request().Context(), deviceID, rawJID, limit)
	if err != nil {
		return gowaError(c, http.StatusInternalServerError, err.Error())
	}
	return gowaSuccess(c, http.StatusOK, map[string]any{"data": messages})
}

// --- React UI Management Handlers (/api/whatsapp/*) ---

type uiChatRecord struct {
	DeviceID        string `json:"deviceId"`
	JID             string `json:"jid"`
	Name            string `json:"name"`
	Archived        bool   `json:"archived"`
	UnreadCount     int    `json:"unreadCount"`
	LastMessage     string `json:"lastMessage"`
	LastMessageTime string `json:"lastMessageTime"`
}

type uiMessageRecord struct {
	ID            string `json:"id"`
	DeviceID      string `json:"deviceId"`
	ChatJID       string `json:"chatJid"`
	SenderJID     string `json:"senderJid"`
	FromMe        bool   `json:"fromMe"`
	MsgType       string `json:"msgType"`
	Body          string `json:"body"`
	MediaObjectID string `json:"mediaObjectId,omitempty"`
	Status        string `json:"status"`
	Timestamp     string `json:"timestamp"`
}

func (m *Manager) handleUIStats(c echo.Context) error {
	stats, err := m.Stats(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, stats)
}

func (m *Manager) handleUIListDevices(c echo.Context) error {
	devices, err := m.ListDevices(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"devices": devices, "count": len(devices)})
}

func (m *Manager) handleUICreateDevice(c echo.Context) error {
	record, err := m.createDeviceFromRequest(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, record)
}

func (m *Manager) handleUIDeleteDevice(c echo.Context) error {
	err := m.DeleteDevice(c.Request().Context(), c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "device not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (m *Manager) handleUILoginQR(c echo.Context) error {
	deviceID := normalizeDeviceID(c.Param("id"))
	loginState, err := m.ConnectQR(c.Request().Context(), deviceID)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	if statusRecord, statusErr := m.GetDeviceStatus(c.Request().Context(), deviceID); statusErr == nil {
		if statusRecord.QR == "" && loginState.QR != "" {
			statusRecord.QR = loginState.QR
			statusRecord.QRLink = loginState.QRLink
			statusRecord.QRDataURL = loginState.QRDataURL
		}
		return c.JSON(http.StatusOK, statusRecord)
	}
	return c.JSON(http.StatusOK, loginState)
}

func (m *Manager) handleUIPairCode(c echo.Context) error {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Phone) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "phone is required"})
	}
	pairResult, err := m.RequestPairingCode(c.Request().Context(), c.Param("id"), body.Phone)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deviceId":    pairResult.DeviceID,
		"phone":       pairResult.Phone,
		"pairCode":    pairResult.PairCode,
		"pairingCode": pairResult.PairingCode,
	})
}

func (m *Manager) handleUILogout(c echo.Context) error {
	deviceID := normalizeDeviceID(c.Param("id"))
	if err := m.Logout(c.Request().Context(), deviceID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"deviceId": deviceID, "status": "logged_out"})
}

func (m *Manager) handleUIReconnect(c echo.Context) error {
	record, err := m.Reconnect(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, record)
}

func (m *Manager) handleUIUpdateConfig(c echo.Context) error {
	var patch DeviceConfigPatch
	if err := c.Bind(&patch); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid config JSON"})
	}
	updated, err := m.UpdateDeviceConfig(c.Request().Context(), c.Param("id"), patch)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

func (m *Manager) handleUIListChats(c echo.Context) error {
	deviceID := firstNonEmpty(c.QueryParam("deviceId"), c.QueryParam("device_id"))
	searchQuery := firstNonEmpty(c.QueryParam("q"), c.QueryParam("query"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	chats, err := m.ListChats(c.Request().Context(), deviceID, searchQuery, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	formatted := make([]uiChatRecord, 0, len(chats))
	for _, chat := range chats {
		formatted = append(formatted, uiChatRecord{
			DeviceID:        chat.DeviceID,
			JID:             chat.JID,
			Name:            chat.Name,
			Archived:        chat.Archived,
			UnreadCount:     chat.UnreadCount,
			LastMessage:     chat.LastMessage,
			LastMessageTime: chat.LastMessageTime,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"chats": formatted, "count": len(formatted)})
}

func (m *Manager) handleUIListMessages(c echo.Context) error {
	deviceID := firstNonEmpty(c.QueryParam("deviceId"), c.QueryParam("device_id"))
	rawJID := c.Param("jid")
	if decoded, err := url.PathUnescape(rawJID); err == nil {
		rawJID = decoded
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	messages, err := m.ListMessages(c.Request().Context(), deviceID, rawJID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	formatted := make([]uiMessageRecord, 0, len(messages))
	for _, msg := range messages {
		formatted = append(formatted, uiMessageRecord{
			ID:            msg.ID,
			DeviceID:      msg.DeviceID,
			ChatJID:       msg.ChatJID,
			SenderJID:     msg.SenderJID,
			FromMe:        msg.FromMe,
			MsgType:       msg.MsgType,
			Body:          msg.Body,
			MediaObjectID: msg.MediaObjectID,
			Status:        msg.Status,
			Timestamp:     msg.Timestamp,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": formatted, "count": len(formatted)})
}

func (m *Manager) handleUISendText(c echo.Context) error {
	var body struct {
		DeviceID  string   `json:"deviceId"`
		Phone     string   `json:"phone"`
		Message   string   `json:"message"`
		ReplyToID string   `json:"replyToId"`
		Mentions  []string `json:"mentions"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid send payload"})
	}
	sent, err := m.SendText(c.Request().Context(), body.DeviceID, body.Phone, body.Message, body.ReplyToID, body.Mentions)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"messageId": sent.MessageID,
		"id":        sent.ID,
		"timestamp": sent.Timestamp,
	})
}

func firstNonEmpty(candidates ...string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseBoolLoose(raw string) bool {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	return normalized == "1" || normalized == "true" || normalized == "yes"
}
