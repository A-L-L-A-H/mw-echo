package comms

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes attaches the Omnichannel Comms Hub endpoints under /api/comms.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}

	grp := api.Group("/comms")
	grp.GET("/channels", m.handleListChannels)
	grp.POST("/channels", m.handleUpsertChannel)
	grp.DELETE("/channels/:id", m.handleDeleteChannel)
	grp.POST("/send", m.handleSend)
	grp.POST("/broadcast", m.handleBroadcast)
	grp.GET("/deliveries", m.handleListDeliveries)
}

func (m *Manager) handleListChannels(c echo.Context) error {
	channelsList, err := m.ListChannels(c.Request().Context())
	if err != nil {
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"channels": channelsList,
		"count":    len(channelsList),
	})
}

func (m *Manager) handleUpsertChannel(c echo.Context) error {
	var body ChannelUpsertInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid channel JSON"})
	}
	channelRec, err := m.UpsertChannel(c.Request().Context(), body)
	if err != nil {
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusCreated, channelRec)
}

func (m *Manager) handleDeleteChannel(c echo.Context) error {
	if err := m.DeleteChannel(c.Request().Context(), c.Param("id")); err != nil {
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (m *Manager) handleSend(c echo.Context) error {
	var body SendInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid send JSON"})
	}
	deliveryRec, err := m.Send(c.Request().Context(), body)
	if err != nil {
		if deliveryRec.ID != "" {
			return c.JSON(http.StatusBadGateway, map[string]any{
				"error":    err.Error(),
				"delivery": deliveryRec,
			})
		}
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusOK, deliveryRec)
}

func (m *Manager) handleBroadcast(c echo.Context) error {
	var body struct {
		Title        string `json:"title"`
		Body         string `json:"body"`
		PriorityTier string `json:"priorityTier"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid broadcast JSON"})
	}
	deliveriesList, err := m.Broadcast(c.Request().Context(), body.Title, body.Body, body.PriorityTier)
	if err != nil {
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deliveries": deliveriesList,
		"count":      len(deliveriesList),
	})
}

func (m *Manager) handleListDeliveries(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	deliveriesList, err := m.ListDeliveries(c.Request().Context(), limit)
	if err != nil {
		return writeCommsError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deliveries": deliveriesList,
		"count":      len(deliveriesList),
	})
}

func writeCommsError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
