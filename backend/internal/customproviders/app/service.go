package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/secrets"
)

// listTimeout bounds one model list when the adapter's policy names none (spec NFR4).
const listTimeout = 30 * time.Second

// Messages of a connection test (spec FR11).
const (
	// WarningNoModelList is the warning of a gateway that answered 404 or 405 to the model list.
	WarningNoModelList = "Model list not available; key not verified"
	messageNoModelList = "OK — model list not available"
	messageKeyRejected = "API key rejected by the provider"
)

// Clock returns the current time.
type Clock func() time.Time

// Deps are the ports of Service. Store, Secrets, References and Adapters are required; Clock
// defaults to time.Now and Log to a logger that discards.
type Deps struct {
	Store      Store
	Secrets    Secrets
	References References
	Adapters   Adapters
	Clock      Clock
	Log        *slog.Logger
}

// Service implements the custom providers use cases.
type Service struct {
	d   Deps
	now Clock
	log *slog.Logger
	// writeMu serializes the writes that change a provider and its secrets (create, update,
	// delete, the key saved by a test), so the read-modify-write of the secrets file and its
	// restore after a failed commit never interleave. It is never held during a network call.
	writeMu sync.Mutex
}

var _ llm.CustomResolver = (*Service)(nil)

// NewService builds the service from its ports; a nil required port is a wiring bug and panics.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Secrets == nil || d.References == nil || d.Adapters == nil {
		panic("customproviders: NewService needs Store, Secrets, References and Adapters")
	}
	s := &Service{d: d, now: d.Clock, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

// HeaderView is an extra header of a provider as the API shows it: the value is never returned.
type HeaderView struct {
	Name            string
	ValueConfigured bool
}

// View is a provider with what is derived on read (spec §7 CustomProvider): plain_http, key_configured
// and per header value_configured.
type View struct {
	Provider      domain.Provider
	PlainHTTP     bool
	KeyConfigured bool
	ExtraHeaders  []HeaderView
}

func viewOf(p domain.Provider, sec secrets.CustomSecret) View {
	v := View{Provider: p, PlainHTTP: p.PlainHTTP(), KeyConfigured: sec.Key != "",
		ExtraHeaders: make([]HeaderView, len(p.ExtraHeaderNames))}
	for i, name := range p.ExtraHeaderNames {
		v.ExtraHeaders[i] = HeaderView{Name: name, ValueConfigured: headerValue(sec, name) != ""}
	}
	return v
}

// headerValue returns the stored value of the header with that name (case-insensitive).
func headerValue(sec secrets.CustomSecret, name string) string {
	for _, h := range sec.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// List returns the workspace's providers sorted by name, then id, in byte order (spec FR2).
func (s *Service) List(ctx context.Context, workspaceID string) ([]View, error) {
	list, err := s.d.Store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list custom providers: %w", err)
	}
	slices.SortFunc(list, func(a, b domain.Provider) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	out := make([]View, len(list))
	for i, p := range list {
		out[i] = viewOf(p, s.d.Secrets.Custom(workspaceID, p.ID))
	}
	return out, nil
}

// Get returns one provider; custom_provider_not_found (404) when the workspace does not define
// it, which includes every malformed id.
func (s *Service) Get(ctx context.Context, workspaceID, id string) (View, error) {
	if _, ok := custom.ParseID(id); !ok {
		return View{}, notFound(id)
	}
	p, err := s.d.Store.Get(ctx, workspaceID, id)
	if err != nil {
		return View{}, s.storeError("get custom provider", id, err)
	}
	return viewOf(p, s.d.Secrets.Custom(workspaceID, id)), nil
}

// Create validates the request and stores the provider, its manual models and its secrets (spec
// FR1, FR2, FR5). The database write and the secrets write form one unit (A7): the secrets are
// written inside the transaction and put back when the commit fails. Errors: 422 validation_error
// (a header without a value is one), 422 custom_provider_limit, 409 custom_provider_exists.
func (s *Service) Create(ctx context.Context, workspaceID string, in domain.Input) (View, error) {
	d, issues := domain.Validate(in)
	if len(issues) > 0 {
		return View{}, validationError(issues)
	}
	headers, issues := domain.MergeHeaders(d.Headers, nil)
	if len(issues) > 0 {
		return View{}, validationError(issues)
	}
	sec := secrets.CustomSecret{Headers: toSecretHeaders(headers)}
	if d.Key.Op == domain.KeyReplace {
		sec.Key = d.Key.Value
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	p := d.Build(now, now)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(workspaceID, p.ID)
	err := s.d.Store.InTx(ctx, func(ctx context.Context) error {
		if err := s.d.Store.Insert(ctx, workspaceID, p, domain.MaxProviders); err != nil {
			return err
		}
		return u.set(sec)
	})
	if err != nil {
		u.run(ctx)
		return View{}, s.storeError("create custom provider", p.ID, err)
	}
	s.log.InfoContext(ctx, "custom provider created", "workspace_id", workspaceID, "provider", p.ID,
		"protocol", string(p.Protocol), "models", len(p.Models), "key_configured", sec.Key != "")
	return viewOf(p, sec), nil
}

// Update replaces every field of the provider except its id (spec FR2, FR5): `api_key` omitted keeps
// the key, null removes it, a string replaces it; a header listed without a value keeps the stored
// value of the header with the same name (case-insensitive), headers not listed are removed with
// their values. Errors: 404 custom_provider_not_found, 422 validation_error.
func (s *Service) Update(ctx context.Context, workspaceID, id string, in domain.Input) (View, error) {
	if _, ok := custom.ParseID(id); !ok {
		return View{}, notFound(id)
	}
	in.ID = id
	d, issues := domain.Validate(in)
	if len(issues) > 0 {
		return View{}, validationError(issues)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(workspaceID, id)
	var out View
	err := s.d.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.d.Store.GetForUpdate(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		old := s.d.Secrets.Custom(workspaceID, id)
		headers, issues := domain.MergeHeaders(d.Headers, fromSecretHeaders(old.Headers))
		if len(issues) > 0 {
			return validationError(issues)
		}
		sec := secrets.CustomSecret{Key: old.Key, Headers: toSecretHeaders(headers)}
		switch d.Key.Op {
		case domain.KeyRemove:
			sec.Key = ""
		case domain.KeyReplace:
			sec.Key = d.Key.Value
		}
		p := d.Build(cur.CreatedAt, s.now().UTC().Truncate(time.Millisecond))
		if err := s.d.Store.Replace(ctx, workspaceID, p); err != nil {
			return err
		}
		if !sameSecret(old, sec) {
			if err := u.set(sec); err != nil {
				return err
			}
		}
		out = viewOf(p, sec)
		return nil
	})
	if err != nil {
		u.run(ctx)
		return View{}, s.storeError("update custom provider", id, err)
	}
	s.log.InfoContext(ctx, "custom provider updated", "workspace_id", workspaceID, "provider", id,
		"models", len(out.Provider.Models), "key_configured", out.KeyConfigured)
	return out, nil
}

// Delete removes the provider, its manual models and its secrets (spec FR2, FR6). The provider row
// is locked FOR UPDATE first (A8), then References is asked inside the same transaction: any agent,
// feature_models entry or default_model that uses it → 409 provider_in_use with details and nothing
// is deleted. It returns the deleted id.
func (s *Service) Delete(ctx context.Context, workspaceID, id string) (string, error) {
	if _, ok := custom.ParseID(id); !ok {
		return "", notFound(id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(workspaceID, id)
	err := s.d.Store.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.d.Store.GetForUpdate(ctx, workspaceID, id); err != nil {
			return err
		}
		usage, err := s.d.References.Find(ctx, workspaceID, id)
		if err != nil {
			return fmt.Errorf("find references: %w", err)
		}
		if !usage.Empty() {
			return inUse(id, usage)
		}
		if err := s.d.Store.Delete(ctx, workspaceID, id); err != nil {
			return err
		}
		return u.set(secrets.CustomSecret{})
	})
	if err != nil {
		u.run(ctx)
		return "", s.storeError("delete custom provider", id, err)
	}
	s.log.InfoContext(ctx, "custom provider deleted", "workspace_id", workspaceID, "provider", id)
	return id, nil
}

// Defined reports whether the workspace defines the provider id (`custom:<slug>`). A malformed id
// is not defined; so is any id when the store fails (the failure is logged): a validator that
// cannot tell refuses the write.
func (s *Service) Defined(ctx context.Context, workspaceID, id string) bool {
	ok, err := s.IsDefined(ctx, workspaceID, id)
	if err != nil {
		s.log.WarnContext(ctx, "custom provider lookup failed", "workspace_id", workspaceID, "provider", id, "error", err.Error())
		return false
	}
	return ok
}

// IsDefined is Defined that returns the store failure.
func (s *Service) IsDefined(ctx context.Context, workspaceID, id string) (bool, error) {
	if _, ok := custom.ParseID(id); !ok {
		return false, nil
	}
	ok, err := s.d.Store.Exists(ctx, workspaceID, id)
	if err != nil {
		return false, fmt.Errorf("look up custom provider %s: %w", id, err)
	}
	return ok, nil
}

// LockDefined is IsDefined that also locks the provider row FOR SHARE until the transaction carried
// by ctx ends (A8). It must run inside a transaction opened by the caller's own transaction runner
// on the same connection.
func (s *Service) LockDefined(ctx context.Context, workspaceID, id string) (bool, error) {
	if _, ok := custom.ParseID(id); !ok {
		return false, nil
	}
	ok, err := s.d.Store.LockDefined(ctx, workspaceID, id)
	if err != nil {
		return false, fmt.Errorf("lock custom provider %s: %w", id, err)
	}
	return ok, nil
}

// Resolve builds the call of custom provider p of workspace ws (llm.CustomResolver): the adapter
// from the Adapters port, the provider's display name, protocol, base-URL host and structured-output mode, a Pricer over its manual
// prices (the built-in price books are never consulted, spec FR12) and the secret values the LLM
// client must scrub. It returns llm.ErrCustomNotConfigured when the workspace does not define p.
func (s *Service) Resolve(ctx context.Context, workspaceID string, p llm.Provider) (llm.CustomCall, error) {
	id := string(p)
	if _, ok := custom.ParseID(id); !ok {
		return llm.CustomCall{}, llm.ErrCustomNotConfigured
	}
	prov, err := s.d.Store.Get(ctx, workspaceID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return llm.CustomCall{}, llm.ErrCustomNotConfigured
	}
	if err != nil {
		return llm.CustomCall{}, fmt.Errorf("load custom provider %s: %w", id, err)
	}
	sec := s.d.Secrets.Custom(workspaceID, id)
	ad, err := s.d.Adapters.Build(prov, sec)
	if err != nil {
		return llm.CustomCall{}, fmt.Errorf("build adapter of custom provider %s: %w", id, err)
	}
	return llm.CustomCall{
		Adapter: ad, Name: prov.Name, Protocol: prov.Protocol, Host: prov.Host(),
		Mode: prov.StructuredOutput, Price: prov, Secrets: secretValues(sec),
	}, nil
}

// TestResult is the outcome of a connection test (settings ConnTestResult without the provider).
// A failed test is data (OK false, a scrubbed Message), not an error.
type TestResult struct {
	OK      bool
	Message string
	// LatencyMS, Model and ModelReachable are set on success only. ModelReachable stays nil
	// when the gateway has no model list.
	LatencyMS      *int
	Model          *string
	ModelReachable *bool
	// Warning is set when the gateway answered 404 or 405 to the model list: the key was not
	// verified (spec FR11).
	Warning *string
}

// TestConnection checks provider id with exactly one model-list request and no completion (spec
// FR11, D5). A key, when given, is saved first and stays saved whatever the outcome. Undefined or
// malformed id → 404 custom_provider_not_found and nothing is saved. Outcomes: a parsable list →
// ok with the model count; 404 or 405 → ok with WarningNoModelList; an address the egress guard
// refuses → egress.ForbiddenMessage; any other failure → the scrubbed, unwrapped message.
func (s *Service) TestConnection(ctx context.Context, workspaceID, id, key string) (TestResult, error) {
	if _, ok := custom.ParseID(id); !ok {
		return TestResult{}, notFound(id)
	}
	if key != "" {
		if msg := domain.ValidateKey(key); msg != "" {
			return TestResult{}, validationError([]domain.Issue{{Code: domain.CodeCustom, Path: []any{"key"}, Message: msg}})
		}
	}
	prov, sec, err := s.loadForTest(ctx, workspaceID, id, key)
	if err != nil {
		return TestResult{}, err
	}
	ad, err := s.d.Adapters.Build(prov, sec)
	if err != nil {
		return TestResult{}, fmt.Errorf("build adapter of custom provider %s: %w", id, err)
	}

	started := s.now()
	models, err := s.listModels(ctx, ad, sec)
	latency := max(0, int(s.now().Sub(started).Milliseconds()))
	if err != nil {
		if ctx.Err() != nil {
			return TestResult{}, fmt.Errorf("test custom provider %s: %w", id, ctx.Err())
		}
		var res TestResult
		var se statusError
		if errors.As(err, &se) && (se.StatusCode() == 404 || se.StatusCode() == 405) {
			warning := WarningNoModelList
			res = TestResult{OK: true, Message: messageNoModelList, LatencyMS: &latency, Warning: &warning}
			res.Model = s.checkedModel(ctx, workspaceID, prov, nil)
		} else {
			res = failedTest(err, sec, key)
		}
		s.log.InfoContext(ctx, "custom provider test", "workspace_id", workspaceID, "provider", id,
			"ok", res.OK, "latency_ms", latency)
		return res, nil
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	res := TestResult{OK: true, Message: fmt.Sprintf("OK — %d models available", len(ids)), LatencyMS: &latency}
	if model := s.checkedModel(ctx, workspaceID, prov, ids); model != nil {
		reachable := slices.Contains(ids, *model)
		res.Model, res.ModelReachable = model, &reachable
	}
	s.log.InfoContext(ctx, "custom provider test", "workspace_id", workspaceID, "provider", id,
		"ok", true, "latency_ms", latency, "models", len(ids))
	return res, nil
}

// loadForTest returns the provider and its secrets, saving key first when given. The lookup and the
// save happen under writeMu, so a provider a concurrent delete removed is never given a secret.
func (s *Service) loadForTest(ctx context.Context, workspaceID, id, key string) (domain.Provider, secrets.CustomSecret, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	prov, err := s.d.Store.Get(ctx, workspaceID, id)
	if err != nil {
		return domain.Provider{}, secrets.CustomSecret{}, s.storeError("get custom provider", id, err)
	}
	sec := s.d.Secrets.Custom(workspaceID, id)
	if key != "" {
		sec.Key = key
		if _, err := s.d.Secrets.SetCustom(workspaceID, id, sec); err != nil {
			return domain.Provider{}, secrets.CustomSecret{}, fmt.Errorf("save key of custom provider %s: %w", id, err)
		}
	}
	return prov, sec, nil
}

// listModels performs the one model-list request, bounded by the adapter's list timeout (30 s).
func (s *Service) listModels(ctx context.Context, ad llm.Adapter, sec secrets.CustomSecret) ([]llm.ModelInfo, error) {
	timeout := ad.Policy().ListTimeout
	if timeout <= 0 {
		timeout = listTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	models, err := ad.ListModels(ctx, sec.Key)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("model list timed out after %s", timeout.Round(time.Millisecond))
	}
	return models, err
}

// failedTest turns a listing failure into an ok=false result with a user-safe message.
func failedTest(err error, sec secrets.CustomSecret, key string) TestResult {
	if egress.IsForbidden(err) {
		return TestResult{Message: egress.ForbiddenMessage}
	}
	var se statusError
	if errors.As(err, &se) && (se.StatusCode() == 401 || se.StatusCode() == 403) {
		// Gateways echo a masked copy of the key on a 401, which an exact scrub misses: the text
		// is never passed on.
		if sec.Key != "" {
			return TestResult{Message: messageKeyRejected}
		}
		return TestResult{Message: fmt.Sprintf("The gateway rejected the request without a key (HTTP %d)", se.StatusCode())}
	}
	return TestResult{Message: scrub(egress.Unwrap(err).Error(), append(secretValues(sec), key))}
}

// checkedModel is the model whose presence in the list a test reports (spec FR11): default_model's
// model when its provider is this one, else the first manual model, else the first listed model,
// else nil.
func (s *Service) checkedModel(ctx context.Context, workspaceID string, prov domain.Provider, listed []string) *string {
	model, ok, err := s.d.References.DefaultModel(ctx, workspaceID, prov.ID)
	switch {
	case err != nil:
		s.log.WarnContext(ctx, "custom provider test: default_model unreadable", "workspace_id", workspaceID,
			"provider", prov.ID, "error", err.Error())
	case ok && model != "":
		return &model
	}
	if len(prov.Models) > 0 {
		m := prov.Models[0].ID
		return &m
	}
	if len(listed) > 0 {
		m := listed[0]
		return &m
	}
	return nil
}

// Models returns the provider's models: the live list (one GET <base_url>/models) merged with the
// manual list, sorted by id in byte order; for an id in both lists the manual label and pricing win,
// and prices the gateway lists are ignored (spec FR10, D23). A failing listing (any status, a
// timeout, an unparsable body, a refused address) gives the manual list alone. An undefined or
// malformed id gives an empty list. Only a store failure or a cancelled ctx is an error.
func (s *Service) Models(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error) {
	if _, ok := custom.ParseID(id); !ok {
		return []llm.ModelInfo{}, nil
	}
	prov, err := s.d.Store.Get(ctx, workspaceID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return []llm.ModelInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load custom provider %s: %w", id, err)
	}
	sec := s.d.Secrets.Custom(workspaceID, id)
	var live []llm.ModelInfo
	ad, err := s.d.Adapters.Build(prov, sec)
	if err == nil {
		live, err = s.listModels(ctx, ad, sec)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("list models of custom provider %s: %w", id, ctx.Err())
		}
		// The class only: gateway text could carry a secret.
		s.log.WarnContext(ctx, "custom provider model list failed, using the manual list",
			"workspace_id", workspaceID, "provider", id, "reason", failureClass(err))
		live = nil
	}
	return mergeModels(prov, live), nil
}

// mergeModels is the union of the live and manual models by id, sorted in byte order.
func mergeModels(prov domain.Provider, live []llm.ModelInfo) []llm.ModelInfo {
	byID := make(map[string]llm.ModelInfo, len(live)+len(prov.Models))
	for _, m := range live {
		if m.ID == "" {
			continue
		}
		if _, dup := byID[m.ID]; dup {
			continue
		}
		// Prices and context length a gateway lists are ignored.
		byID[m.ID] = llm.ModelInfo{ID: m.ID, Provider: llm.Provider(prov.ID), Created: m.Created, Label: m.Label}
	}
	for _, m := range prov.Models {
		info := byID[m.ID]
		info.ID, info.Provider = m.ID, llm.Provider(prov.ID)
		info.Label, info.Pricing = m.Label, nil
		if m.Pricing != nil {
			info.Pricing = &llm.ModelPricing{PromptPerM: m.Pricing.PromptPerM, CompletionPerM: m.Pricing.CompletionPerM}
		}
		byID[m.ID] = info
	}
	out := make([]llm.ModelInfo, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b llm.ModelInfo) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// statusError is an adapter error that carries the HTTP status of the gateway's answer.
type statusError interface{ StatusCode() int }

// failureClass names why a listing failed, without any gateway text.
func failureClass(err error) string {
	var se statusError
	switch {
	case egress.IsForbidden(err):
		return "forbidden_address"
	case errors.As(err, &se):
		return fmt.Sprintf("http_%d", se.StatusCode())
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}

// storeError maps the store's sentinels to application errors; an *apperr.Error passes unchanged and
// any other error is wrapped (a 500 at the edge).
func (s *Service) storeError(op, id string, err error) error {
	var ae *apperr.Error
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, domain.ErrNotFound):
		return notFound(id).WithCause(err)
	case errors.Is(err, domain.ErrExists):
		return exists(id, err)
	case errors.Is(err, domain.ErrLimit):
		return limitReached(err)
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

// undo records a secrets write so that it can be put back when the database transaction that
// surrounds it does not commit (A7).
type undo struct {
	s               *Service
	workspaceID, id string
	prev            secrets.CustomSecret
	armed           bool
}

func (s *Service) undo(workspaceID, id string) *undo {
	return &undo{s: s, workspaceID: workspaceID, id: id}
}

// set writes sec as the provider's secrets and arms the undo. A zero sec removes the entry.
func (u *undo) set(sec secrets.CustomSecret) error {
	var (
		prev secrets.CustomSecret
		err  error
	)
	if sec.IsZero() {
		prev, err = u.s.d.Secrets.DeleteCustom(u.workspaceID, u.id)
	} else {
		prev, err = u.s.d.Secrets.SetCustom(u.workspaceID, u.id, sec)
	}
	if err != nil {
		return fmt.Errorf("write secrets of custom provider %s: %w", u.id, err)
	}
	u.prev, u.armed = prev, true
	return nil
}

// run puts the previous secrets back if a write was made. A failed restore is logged at ERROR with
// the provider id only; the secrets store never puts a value into an error.
func (u *undo) run(ctx context.Context) {
	if !u.armed {
		return
	}
	if err := u.s.d.Secrets.RestoreCustom(u.workspaceID, u.id, u.prev); err != nil {
		u.s.log.ErrorContext(ctx, "custom provider secrets could not be restored after a failed write",
			"workspace_id", u.workspaceID, "provider", u.id, "error", err.Error())
	}
}

func toSecretHeaders(in []domain.HeaderValue) []secrets.CustomHeader {
	if len(in) == 0 {
		return nil
	}
	out := make([]secrets.CustomHeader, len(in))
	for i, h := range in {
		out[i] = secrets.CustomHeader{Name: h.Name, Value: h.Value}
	}
	return out
}

func fromSecretHeaders(in []secrets.CustomHeader) []domain.HeaderValue {
	out := make([]domain.HeaderValue, len(in))
	for i, h := range in {
		out[i] = domain.HeaderValue{Name: h.Name, Value: h.Value}
	}
	return out
}

// sameSecret reports whether two entries hold the same key and the same headers in the same order.
func sameSecret(a, b secrets.CustomSecret) bool {
	return a.Key == b.Key && slices.Equal(a.Headers, b.Headers)
}

// secretValues lists the non-empty secret values of an entry: the key and the header values.
func secretValues(sec secrets.CustomSecret) []string {
	var out []string
	if sec.Key != "" {
		out = append(out, sec.Key)
	}
	for _, h := range sec.Headers {
		if h.Value != "" {
			out = append(out, h.Value)
		}
	}
	return out
}
