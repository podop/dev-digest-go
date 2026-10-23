// Package evalshttp serves the 16 eval routes (spec section 7): eval cases (from a finding, by
// hand, list, edit, delete), eval runs (start for an agent or a skill, run-all, lists, detail,
// compare) and the two dashboards. It is the only place of the context that knows Echo, status
// codes and the wire shapes; it depends on the use cases only through app.Cases and app.Runs.
//
// Requests are validated the way the Node Zod schemas did (dto.go): path ids must be UUIDs, bodies
// arrive only through httpx.ReadJSONBody, and a failed check answers 422 with one issue per check.
package evalshttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// Registered paths of the rate-limited routes: the composition root gives them their buckets
// (RateLimitBuckets) under exactly these paths.
const (
	AgentRunsPath = "/agents/:id/eval-runs"
	SkillRunsPath = "/skills/:id/eval-runs"
	RunAllPath    = "/eval-runs/run-all"
)

// Rate limits (Node parity): the agent and the skill run starts draw on ONE budget of 10 requests
// a minute; run-all has a budget of its own, also 10 a minute.
const (
	// StartBucketName is the shared counter of the two single-owner run starts.
	StartBucketName = "eval-run-start"
	StartLimit      = 10
	RunAllLimit     = 10
)

// RateLimitBuckets are the budgets these routes need; pass them in httpx.RateLimit.Buckets when
// building the router. The two starts share one counter keyed by a constant (the API has one
// workspace), run-all counts per client like any bucket without a key.
func RateLimitBuckets() []httpx.Bucket {
	workspace := func(*echo.Context) string { return "workspace" }
	return []httpx.Bucket{
		{Method: http.MethodPost, Path: AgentRunsPath, Limit: StartLimit, Name: StartBucketName, Key: workspace},
		{Method: http.MethodPost, Path: SkillRunsPath, Limit: StartLimit, Name: StartBucketName, Key: workspace},
		{Method: http.MethodPost, Path: RunAllPath, Limit: RunAllLimit},
	}
}

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	cases app.Cases
	runs  app.Runs
	ids   identities
}

// AddRoutes registers the eval routes:
//
//	POST   /findings/:id/eval-case      201 AgentEvalCase (no body)
//	GET    /agents/:id/eval-cases       {cases}
//	POST   /agents/:id/eval-cases       201 AgentEvalCase, body EvalCaseCreateInput
//	GET    /skills/:id/eval-cases       {cases}
//	POST   /skills/:id/eval-cases       201 AgentEvalCase, body EvalCaseCreateInput
//	PATCH  /eval-cases/:id              AgentEvalCase, body AgentEvalCaseUpdateInput
//	DELETE /eval-cases/:id              204, empty body
//	POST   /agents/:id/eval-runs        202 EvalRunSummary (no body; shared start bucket)
//	GET    /agents/:id/eval-runs        {runs}, ?limit=&period=
//	POST   /skills/:id/eval-runs        202 EvalRunSummary, body {agent_id} (shared start bucket)
//	GET    /skills/:id/eval-runs        {runs}, ?limit=
//	GET    /eval-runs/compare           EvalCompare, ?a=&b=
//	GET    /eval-runs/:id               {run, results}
//	POST   /eval-runs/run-all           202 {started, skipped} (own bucket)
//	GET    /eval/dashboard              EvalOverview
//	GET    /agents/:id/eval/dashboard   EvalAgentDashboard, ?period=
//
// The bodiless POSTs never read the request body. GET handlers stay read-only: Echo runs them for
// HEAD too. Rate limiting is the router's (httpx.RateLimit with RateLimitBuckets).
func AddRoutes(e *echo.Echo, cases app.Cases, runs app.Runs, ids identities) {
	h := &handler{cases: cases, runs: runs, ids: ids}
	e.POST("/findings/:id/eval-case", h.caseFromFinding)
	e.GET("/agents/:id/eval-cases", h.listCases(domain.OwnerAgent))
	e.POST("/agents/:id/eval-cases", h.createCase(domain.OwnerAgent))
	e.GET("/skills/:id/eval-cases", h.listCases(domain.OwnerSkill))
	e.POST("/skills/:id/eval-cases", h.createCase(domain.OwnerSkill))
	e.PATCH("/eval-cases/:id", h.updateCase)
	e.DELETE("/eval-cases/:id", h.deleteCase)
	e.POST(AgentRunsPath, h.startAgentRun)
	e.GET("/agents/:id/eval-runs", h.listAgentRuns)
	e.POST(SkillRunsPath, h.startSkillRun)
	e.GET("/skills/:id/eval-runs", h.listSkillRuns)
	e.GET("/eval-runs/compare", h.compare)
	e.GET("/eval-runs/:id", h.runDetail)
	e.POST(RunAllPath, h.runAll)
	e.GET("/eval/dashboard", h.overview)
	e.GET("/agents/:id/eval/dashboard", h.agentDashboard)
}

// workspace resolves the workspace of the request.
func (h *handler) workspace(c *echo.Context) (string, error) {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return ident.WorkspaceID, nil
}

// body reads the JSON body (httpx.ReadJSONBody) and runs parse over it; issues are a 422.
func body[T any](c *echo.Context, parse func([]byte) (T, []httpx.Issue)) (T, error) {
	var zero T
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return zero, err
	}
	v, issues := parse(raw)
	if len(issues) > 0 {
		return zero, httpx.NewValidationError(issues...)
	}
	return v, nil
}

func (h *handler) caseFromFinding(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	created, err := h.cases.FromFinding(c.Request().Context(), ws, id)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusCreated, toCase(created))
}

func (h *handler) listCases(kind domain.OwnerKind) echo.HandlerFunc {
	return func(c *echo.Context) error {
		id, err := httpx.PathUUID(c, "id")
		if err != nil {
			return err
		}
		ws, err := h.workspace(c)
		if err != nil {
			return err
		}
		views, err := h.cases.List(c.Request().Context(), ws, kind, id)
		if err != nil {
			return mapError(err)
		}
		return c.JSON(http.StatusOK, toCaseList(views))
	}
}

func (h *handler) createCase(kind domain.OwnerKind) echo.HandlerFunc {
	return func(c *echo.Context) error {
		id, err := httpx.PathUUID(c, "id")
		if err != nil {
			return err
		}
		in, err := body(c, parseCreate)
		if err != nil {
			return err
		}
		ws, err := h.workspace(c)
		if err != nil {
			return err
		}
		created, err := h.cases.Create(c.Request().Context(), ws, kind, id, in)
		if err != nil {
			return mapError(err)
		}
		return c.JSON(http.StatusCreated, toCase(created))
	}
}

func (h *handler) updateCase(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	in, err := body(c, parseUpdate)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	updated, err := h.cases.Update(c.Request().Context(), ws, id, in)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toCase(updated))
}

func (h *handler) deleteCase(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	if err := h.cases.Delete(c.Request().Context(), ws, id); err != nil {
		return mapError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handler) startAgentRun(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	run, err := h.runs.StartAgent(c.Request().Context(), ws, id)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusAccepted, toRun(run))
}

func (h *handler) startSkillRun(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	hostAgent, err := body(c, parseSkillRunStart)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	run, err := h.runs.StartSkill(c.Request().Context(), ws, id, hostAgent)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusAccepted, toRun(run))
}

func (h *handler) listAgentRuns(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	q, issues := parseRunListQuery(c)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	runs, err := h.runs.ListAgentRuns(c.Request().Context(), ws, id, q.limit, q.period)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toRunList(runs))
}

func (h *handler) listSkillRuns(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	q, issues := parseRunListQuery(c) // the period is validated but a skill's runs ignore it
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	runs, err := h.runs.ListSkillRuns(c.Request().Context(), ws, id, q.limit)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toRunList(runs))
}

func (h *handler) compare(c *echo.Context) error {
	a, b, issues := parseCompareQuery(c)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	cmp, err := h.runs.Compare(c.Request().Context(), ws, a, b)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toCompare(cmp))
}

func (h *handler) runDetail(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	detail, err := h.runs.Detail(c.Request().Context(), ws, id)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toRunDetail(detail))
}

func (h *handler) runAll(c *echo.Context) error {
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	res, err := h.runs.RunAll(c.Request().Context(), ws)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusAccepted, toRunAll(res))
}

func (h *handler) overview(c *echo.Context) error {
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	o, err := h.runs.Overview(c.Request().Context(), ws)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toOverview(o))
}

func (h *handler) agentDashboard(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	period, issues := periodQuery(c)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	d, err := h.runs.AgentDashboard(c.Request().Context(), ws, id, period)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toAgentDashboard(d))
}
