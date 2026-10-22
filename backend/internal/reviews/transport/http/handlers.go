package reviewshttp

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

// start answers POST /pulls/:id/review. The body is validated first (422), then the use case
// resolves targets (400, 404 agent), the pull request and repo (404) and writes.
func (h *handler) start(c *echo.Context) error {
	prID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	req, issues := parseStartBody(raw)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	res, err := h.api.Start(c.Request().Context(), ws, prID, req)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toStartResponse(res))
}

func (h *handler) activeRuns(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	runs, err := h.api.ActiveRuns(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toActiveRuns(runs))
}

func (h *handler) listRuns(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	runs, err := h.api.ListRuns(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toRunSummaries(runs))
}

// deleteRun answers {"ok":false} for a run that does not exist.
func (h *handler) deleteRun(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	deleted, err := h.api.DeleteRun(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okDTO{OK: deleted})
}

// cancelRun never reads the request body: whatever a caller sends is ignored.
func (h *handler) cancelRun(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	if err := h.api.CancelRun(c.Request().Context(), ws, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okDTO{OK: true})
}

func (h *handler) trace(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	tr, err := h.api.Trace(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toTrace(tr))
}

func (h *handler) reviews(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	rs, err := h.api.Reviews(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toReviews(rs))
}

func (h *handler) deleteReview(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	if err := h.api.DeleteReview(c.Request().Context(), ws, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, okDTO{OK: true})
}

// acceptFinding and dismissFinding never read the request body (the UI sends {reply}, which the
// Node API ignores).
func (h *handler) acceptFinding(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	f, err := h.api.AcceptFinding(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, findingResponse{Finding: toFinding(f)})
}

func (h *handler) dismissFinding(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	f, err := h.api.DismissFinding(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, findingResponse{Finding: toFinding(f)})
}

func (h *handler) multiAgentRun(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	run, err := h.api.MultiAgentRun(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toMultiAgentRun(run))
}

// listMultiAgentRuns validates only that limit is an integer (422); the range 1-20 is the use
// case's 400 invalid_limit.
func (h *handler) listMultiAgentRuns(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	limit, issues := parseLimit(c)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	runs, err := h.api.ListMultiAgentRuns(c.Request().Context(), ws, id, limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toMultiAgentSummaries(runs))
}

func (h *handler) estimates(c *echo.Context) error {
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	es, err := h.api.Estimates(c.Request().Context(), ws)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toEstimates(es))
}

func (h *handler) learn(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	res, err := h.api.Learn(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	f := toFinding(res.Finding)
	mem := res.MemoryID
	f.MemoryID = &memoryRef{ID: &mem}
	return c.JSON(http.StatusOK, learnResponse{Finding: f, MemoryID: res.MemoryID})
}
