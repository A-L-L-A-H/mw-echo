package vaultenv

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type revealSecretRequest struct {
	Project string `json:"project"`
	EnvName string `json:"envName"`
	KeyName string `json:"keyName"`
	Key     string `json:"key"`
}

type backupRequest struct {
	Project string `json:"project"`
	EnvName string `json:"envName"`
}

// RegisterRoutes mounts the encrypted .env vault REST endpoints under /api/vaultenv.
func (v *Vault) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}
	group := api.Group("/vaultenv")
	group.GET("/keys", v.handleListKeys)
	group.PUT("/secret", v.handleSetSecret)
	group.POST("/import", v.handleImportDotEnv)
	group.POST("/reveal", v.handleRevealSecret)
	group.GET("/export", v.handleExportDotEnv)
	group.DELETE("/secret", v.handleDeleteSecret)
	group.GET("/audit", v.handleListAuditLogs)
	group.POST("/backup", v.handleBackupEncrypted)
}

func (v *Vault) handleListKeys(c echo.Context) error {
	keys, err := v.ListKeys(c.Request().Context(), c.QueryParam("project"), c.QueryParam("env"))
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"keys":  keys,
		"count": len(keys),
	})
}

func (v *Vault) handleSetSecret(c echo.Context) error {
	var body SetSecretInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid secret JSON payload"})
	}
	meta, err := v.SetSecret(c.Request().Context(), body)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, meta)
}

func (v *Vault) handleImportDotEnv(c echo.Context) error {
	var body BulkImportInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid import JSON payload"})
	}
	if body.Project == "" {
		body.Project = c.QueryParam("project")
	}
	if body.EnvName == "" {
		body.EnvName = c.QueryParam("env")
	}
	importResult, err := v.BulkImportDotEnv(c.Request().Context(), body.Project, body.EnvName, body.RawDotEnv)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, importResult)
}

func (v *Vault) handleRevealSecret(c echo.Context) error {
	var body revealSecretRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid reveal JSON payload"})
	}
	keyName := body.KeyName
	if keyName == "" {
		keyName = body.Key
	}
	revealed, err := v.RevealSecret(c.Request().Context(), body.Project, body.EnvName, keyName)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, revealed)
}

func (v *Vault) handleExportDotEnv(c echo.Context) error {
	project, envName, dotenvContent, keyCount, err := v.exportDotEnvWithCount(
		c.Request().Context(),
		c.QueryParam("project"),
		c.QueryParam("env"),
	)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"project":  project,
		"envName":  envName,
		"dotenv":   dotenvContent,
		"keyCount": keyCount,
	})
}

func (v *Vault) handleDeleteSecret(c echo.Context) error {
	err := v.DeleteSecret(
		c.Request().Context(),
		c.QueryParam("project"),
		c.QueryParam("env"),
		c.QueryParam("key"),
	)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (v *Vault) handleListAuditLogs(c echo.Context) error {
	limit := 0
	if rawLimit := c.QueryParam("limit"); rawLimit != "" {
		parsedLimit, convErr := strconv.Atoi(rawLimit)
		if convErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid limit query parameter"})
		}
		limit = parsedLimit
	}
	logs, err := v.ListAuditLogs(c.Request().Context(), c.QueryParam("project"), limit)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"logs":  logs,
		"count": len(logs),
	})
}

func (v *Vault) handleBackupEncrypted(c echo.Context) error {
	var body backupRequest
	if c.Request().Body != nil && c.Request().ContentLength != 0 {
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid backup JSON payload"})
		}
	}
	if body.Project == "" {
		body.Project = c.QueryParam("project")
	}
	if body.EnvName == "" {
		body.EnvName = c.QueryParam("env")
	}
	snapshot, err := v.BackupEncryptedToPool(c.Request().Context(), body.Project, body.EnvName)
	if err != nil {
		return writeVaultError(c, err)
	}
	return c.JSON(http.StatusOK, snapshot)
}

func writeVaultError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrBackupUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
