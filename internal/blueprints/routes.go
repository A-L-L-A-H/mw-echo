package blueprints

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes attaches the PaaS blueprint catalog endpoints under /api/blueprints.
func (c *Catalog) RegisterRoutes(api *echo.Group) {
	grp := api.Group("/blueprints")

	grp.GET("", c.handleSearchBlueprints)
	grp.GET("/:id", c.handleGetBlueprint)
	grp.GET("/:id/compose", c.handleGetCompose)
	grp.POST("/sniff", c.handleSniffUpstream)
}

func (c *Catalog) handleSearchBlueprints(ctx echo.Context) error {
	query := ctx.QueryParam("q")
	source := ctx.QueryParam("source")
	category := ctx.QueryParam("category")
	limit := 100
	if rawLimit := ctx.QueryParam("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	reqCtx := ctx.Request().Context()
	blueprints, err := c.Search(reqCtx, query, source, category, limit)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	sources, err := c.ListSources(reqCtx)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"blueprints": blueprints,
		"count":      len(blueprints),
		"sources":    sources,
	})
}

func (c *Catalog) handleGetBlueprint(ctx echo.Context) error {
	blueprint, err := c.Get(ctx.Request().Context(), ctx.Param("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return ctx.JSON(http.StatusOK, blueprint)
}

func (c *Catalog) handleGetCompose(ctx echo.Context) error {
	composeYAML, filename, err := c.GetCompose(ctx.Request().Context(), ctx.Param("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	ctx.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
	return ctx.Blob(http.StatusOK, "text/yaml; charset=utf-8", []byte(composeYAML))
}

func (c *Catalog) handleSniffUpstream(ctx echo.Context) error {
	reqCtx := ctx.Request().Context()
	results, err := c.SniffAll(reqCtx)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	totalBlueprints, err := c.Count(reqCtx)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"results":         results,
		"totalBlueprints": totalBlueprints,
	})
}
