package meshfleet

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

type joinCommandRequest struct {
	NetworkName string `json:"networkName"`
	PeerAddr    string `json:"peerAddr"`
	Hostname    string `json:"hostname"`
}

type createWorktreeRequest struct {
	RepoDir   string `json:"repoDir"`
	Branch    string `json:"branch"`
	TargetDir string `json:"targetDir"`
}

// RegisterRoutes mounts the private mesh fleet and git worktree endpoints under /api/mesh.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}

	meshGroup := api.Group("/mesh")
	meshGroup.GET("/nodes", m.handleListNodes)
	meshGroup.POST("/nodes", m.handleRegisterNode)
	meshGroup.DELETE("/nodes/:id", m.handleDeleteNode)
	meshGroup.POST("/nodes/:id/probe", m.handleProbeNode)
	meshGroup.POST("/join-command", m.handleBuildJoinCommand)
	meshGroup.GET("/worktrees", m.handleListWorktrees)
	meshGroup.POST("/worktrees", m.handleCreateWorktree)
	meshGroup.DELETE("/worktrees", m.handleRemoveWorktree)
}

func (m *Manager) handleListNodes(c echo.Context) error {
	nodesList, err := m.ListNodes(c.Request().Context())
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"nodes": nodesList,
		"count": len(nodesList),
	})
}

func (m *Manager) handleRegisterNode(c echo.Context) error {
	var body RegisterNodeInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid mesh node JSON payload"})
	}
	nodeRecord, err := m.RegisterNode(c.Request().Context(), body)
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusCreated, nodeRecord)
}

func (m *Manager) handleDeleteNode(c echo.Context) error {
	nodeID := c.Param("id")
	if err := m.DeleteNode(c.Request().Context(), nodeID); err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deleted": true,
		"id":      nodeID,
	})
}

func (m *Manager) handleProbeNode(c echo.Context) error {
	probeResult, err := m.ProbeNode(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, probeResult)
}

func (m *Manager) handleBuildJoinCommand(c echo.Context) error {
	var body joinCommandRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid join-command JSON payload"})
	}
	joinCmd, err := BuildVPSJoinCommand(body.NetworkName, body.PeerAddr, body.Hostname)
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"networkName": body.NetworkName,
		"peerAddr":    body.PeerAddr,
		"hostname":    body.Hostname,
		"command":     joinCmd,
	})
}

func (m *Manager) handleListWorktrees(c echo.Context) error {
	repoDir := c.QueryParam("repoDir")
	worktreesList, err := ListWorktrees(c.Request().Context(), repoDir)
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"repoDir":   repoDir,
		"worktrees": worktreesList,
		"count":     len(worktreesList),
	})
}

func (m *Manager) handleCreateWorktree(c echo.Context) error {
	var body createWorktreeRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid worktree JSON payload"})
	}
	worktreeInfo, err := CreateWorktree(c.Request().Context(), body.RepoDir, body.Branch, body.TargetDir)
	if err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusCreated, worktreeInfo)
}

func (m *Manager) handleRemoveWorktree(c echo.Context) error {
	repoDir := c.QueryParam("repoDir")
	targetDir := c.QueryParam("targetDir")
	if err := RemoveWorktree(c.Request().Context(), repoDir, targetDir); err != nil {
		return writeMeshError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deleted":   true,
		"targetDir": targetDir,
	})
}

func writeMeshError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrEndpointDenied), errors.Is(err, ErrPathEscape):
		return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
