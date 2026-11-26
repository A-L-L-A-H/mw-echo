package worldradar

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes attaches all R!OTS World Radar Network REST endpoints under /api/radar.
func (e *Engine) RegisterRoutes(api *echo.Group) {
	grp := api.Group("/radar")

	grp.GET("/domains", e.handleListDomains)
	grp.GET("/domains/:domainId/niches", e.handleGetDomainNiches)
	grp.GET("/lenses", e.handleListLenses)
	grp.GET("/packs", e.handleListPacks)
	grp.POST("/packs/:packId/install", e.handleInstallPack)

	grp.GET("/instances", e.handleListInstances)
	grp.POST("/instances", e.handleCreateInstance)
	grp.GET("/instances/:id", e.handleGetInstance)
	grp.PATCH("/instances/:id", e.handleUpdateInstance)
	grp.POST("/instances/:id/targets", e.handleAddTarget)
	grp.POST("/instances/:id/run", e.handleRunInstance)
	grp.POST("/instances/:id/pause", e.handlePauseInstance)

	grp.GET("/signals", e.handleListSignals)
	grp.GET("/events", e.handleListEvents)
	grp.GET("/events/:id", e.handleGetEventDetail)
	grp.GET("/entities", e.handleListEntities)
	grp.GET("/entities/:id/relationships", e.handleListEntityRelationships)
	grp.GET("/graph", e.handleGetGraph)

	grp.GET("/proposals", e.handleListProposals)
	grp.POST("/proposals", e.handleCreateProposal)
	grp.POST("/proposals/:id/approve", e.handleApproveProposal)
	grp.POST("/proposals/:id/reject", e.handleRejectProposal)

	grp.GET("/alerts", e.handleListAlerts)
	grp.POST("/alerts/:id/acknowledge", e.handleAcknowledgeAlert)

	grp.GET("/reports", e.handleListReports)
	grp.POST("/reports", e.handleGenerateReport)

	grp.GET("/sources/health", e.handleListSourcesHealth)
	grp.POST("/feedback", e.handleSubmitFeedback)
}

func (e *Engine) handleListDomains(c echo.Context) error {
	domains := AllDomains()
	return c.JSON(http.StatusOK, map[string]any{
		"taxonomyVersion": TaxonomyVersion,
		"domains":         domains,
		"count":           len(domains),
	})
}

func (e *Engine) handleGetDomainNiches(c echo.Context) error {
	dom, ok := GetDomain(c.Param("domainId"))
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "domain not found"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"domainId": dom.ID,
		"name":     dom.Name,
		"niches":   dom.Niches,
		"count":    len(dom.Niches),
	})
}

func (e *Engine) handleListLenses(c echo.Context) error {
	lenses := AllLenses()
	return c.JSON(http.StatusOK, map[string]any{
		"taxonomyVersion": TaxonomyVersion,
		"lenses":          lenses,
		"count":           len(lenses),
	})
}

func (e *Engine) handleListPacks(c echo.Context) error {
	packs := AllPacks()
	return c.JSON(http.StatusOK, map[string]any{
		"taxonomyVersion": TaxonomyVersion,
		"packs":           packs,
		"count":           len(packs),
	})
}

func (e *Engine) handleInstallPack(c echo.Context) error {
	var body struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	_ = c.Bind(&body)
	inst, err := e.InstallPack(c.Request().Context(), c.Param("packId"), body.Name, body.Mode)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, inst)
}

func (e *Engine) handleListInstances(c echo.Context) error {
	instances, err := e.ListInstances(c.Request().Context())
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"instances": instances, "count": len(instances)})
}

func (e *Engine) handleCreateInstance(c echo.Context) error {
	var body CreateInstanceInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	inst, err := e.CreateInstance(c.Request().Context(), body)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, inst)
}

func (e *Engine) handleGetInstance(c echo.Context) error {
	inst, err := e.GetInstance(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeEngineError(c, err)
	}
	targets, err := e.ListWatchTargets(c.Request().Context(), inst.ID)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"instance": inst,
		"targets":  targets,
	})
}

func (e *Engine) handleUpdateInstance(c echo.Context) error {
	var body UpdateInstanceInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	inst, err := e.UpdateInstance(c.Request().Context(), c.Param("id"), body)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, inst)
}

func (e *Engine) handleAddTarget(c echo.Context) error {
	var body WatchTargetInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	target, err := e.AddWatchTarget(c.Request().Context(), c.Param("id"), body)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, target)
}

func (e *Engine) handleRunInstance(c echo.Context) error {
	summary, err := e.RunInstance(c.Request().Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrInstancePaused) {
			return c.JSON(http.StatusConflict, map[string]any{"error": err.Error(), "summary": summary})
		}
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, summary)
}

func (e *Engine) handlePauseInstance(c echo.Context) error {
	inst, err := e.PauseInstance(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, inst)
}

func (e *Engine) handleListSignals(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 50)
	signals, err := e.ListSignals(c.Request().Context(), c.QueryParam("instanceId"), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"signals": signals, "count": len(signals)})
}

func (e *Engine) handleListEvents(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 50)
	events, err := e.ListEvents(c.Request().Context(), c.QueryParam("domain"), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"events": events, "count": len(events)})
}

func (e *Engine) handleGetEventDetail(c echo.Context) error {
	detail, err := e.GetEventDetail(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, detail)
}

func (e *Engine) handleListEntities(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 100)
	entities, err := e.ListEntities(c.Request().Context(), c.QueryParam("reviewState"), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"entities": entities, "count": len(entities)})
}

func (e *Engine) handleListEntityRelationships(c echo.Context) error {
	rels, err := e.ListEntityRelationships(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"relationships": rels, "count": len(rels)})
}

func (e *Engine) handleListAlerts(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 50)
	alerts, err := e.ListAlerts(c.Request().Context(), c.QueryParam("status"), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"alerts": alerts, "count": len(alerts)})
}

func (e *Engine) handleAcknowledgeAlert(c echo.Context) error {
	var body struct {
		Actor string `json:"actor"`
	}
	_ = c.Bind(&body)
	alt, err := e.AcknowledgeAlert(c.Request().Context(), c.Param("id"), body.Actor)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, alt)
}

func (e *Engine) handleListReports(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 25)
	reports, err := e.ListReports(c.Request().Context(), c.QueryParam("instanceId"), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"reports": reports, "count": len(reports)})
}

func (e *Engine) handleGenerateReport(c echo.Context) error {
	var body struct {
		InstanceID string `json:"instanceId"`
		Title      string `json:"title"`
		Scope      string `json:"scope"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	report, err := e.GenerateReport(c.Request().Context(), body.InstanceID, body.Title, body.Scope)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, report)
}

func (e *Engine) handleListSourcesHealth(c echo.Context) error {
	healthList, err := e.ListConnectorHealth(c.Request().Context())
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"sources": healthList, "count": len(healthList)})
}

func (e *Engine) handleSubmitFeedback(c echo.Context) error {
	var body FeedbackInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	fb, err := e.SubmitFeedback(c.Request().Context(), body)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, fb)
}

func (e *Engine) handleGetGraph(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), 100)
	graph, err := e.BuildEntityGraph(c.Request().Context(), limit)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, graph)
}

func (e *Engine) handleListProposals(c echo.Context) error {
	proposals, err := e.ListProposals(c.Request().Context(), c.QueryParam("instanceId"), c.QueryParam("status"))
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"proposals": proposals, "count": len(proposals)})
}

func (e *Engine) handleCreateProposal(c echo.Context) error {
	var body ProposalInput
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	prop, err := e.ProposeTarget(c.Request().Context(), body)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusCreated, prop)
}

func (e *Engine) handleApproveProposal(c echo.Context) error {
	prop, err := e.ApproveProposal(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, prop)
}

func (e *Engine) handleRejectProposal(c echo.Context) error {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.Bind(&body)
	prop, err := e.RejectProposal(c.Request().Context(), c.Param("id"), body.Reason)
	if err != nil {
		return writeEngineError(c, err)
	}
	return c.JSON(http.StatusOK, prop)
}

func writeEngineError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrInstancePaused):
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
