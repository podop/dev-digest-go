package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

// Component tests of the custom model provider routes (plan S27: AC1, AC2, AC6-AC8, AC10, AC11,
// AC14, AC16, AC42) through the real server: the CRUD round trip, the header merge on update, the
// delete guard, the defined and undefined ids of settings and agents, the model list, the
// connection test, the extra CA, the request guards and the secrets leak check. The gateway is an
// httptest server on loopback (allowed). The goldens the UI contract test validates are written by
// TestContracts_CustomProviders. Helpers are prefixed cp (the call and CI tests share the package).

const (
	cpKey       = "sk-SYNAPSE-SECRET-1"
	cpTenant    = "TENANT-SECRET-9"
	cpTeam      = "TEAM-SECRET-7"
	cpEvil      = "http://evil.example"
	cpProviders = "/settings/custom-providers"
)

// ---- gateway stub ----

// cpReq is one request the gateway saw.
type cpReq struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// cpGateway plays an OpenAI-compatible gateway and records every request.
type cpGateway struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []cpReq
	h    http.HandlerFunc
	// release unblocks handlers that wait for the client to give up.
	release chan struct{}
}

func newCPGateway(t *testing.T, h http.HandlerFunc) *cpGateway {
	t.Helper()
	g := &cpGateway{h: h, release: make(chan struct{})}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	t.Cleanup(func() { close(g.release) }) // runs before Close: a blocked handler would stall it
	return g
}

func newCPTLSGateway(t *testing.T, h http.HandlerFunc) *cpGateway {
	t.Helper()
	g := &cpGateway{h: h, release: make(chan struct{})}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	t.Cleanup(func() { close(g.release) })
	return g
}

func (g *cpGateway) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.reqs = append(g.reqs, cpReq{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	h := g.h
	g.mu.Unlock()
	h(w, r)
}

// base is the base URL a provider gets: the API root of the stub.
func (g *cpGateway) base() string { return g.srv.URL + "/v1" }

func (g *cpGateway) requests() []cpReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

func (g *cpGateway) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = nil
}

// set swaps the handler between steps of a test.
func (g *cpGateway) set(h http.HandlerFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.h = h
}

// cpListing answers GET /v1/models with the given ids.
func cpListing(ids ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data := make([]map[string]any, len(ids))
		for i, id := range ids {
			data[i] = map[string]any{"id": id, "object": "model"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}
}

// cpStatus answers every request with a status and a body.
func cpStatus(code int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

// ---- server and calls ----

// cpStack is a running server whose every answer and log line is scanned for the secrets the test
// registered, when the test ends.
type cpStack struct {
	*stack
	env *sbEnv

	mu      sync.Mutex
	bodies  []string
	secrets []string
}

// cpBoot starts the server over a fresh database with the Host allow-list and the fixed clock.
func cpBoot(t *testing.T, mutate ...func(*spec)) *cpStack {
	t.Helper()
	return cpBootOn(t, newSBEnv(t), mutate...)
}

func cpBootOn(t *testing.T, e *sbEnv, mutate ...func(*spec)) *cpStack {
	t.Helper()
	sp := spec{db: e.db, noSeed: true, now: func() time.Time { return sbNow }, allowedHosts: sbHosts}
	for _, m := range mutate {
		m(&sp)
	}
	s := &cpStack{stack: startStack(t, sp), env: e}
	t.Cleanup(func() { s.assertClean(t) })
	return s
}

// leakCheck registers values that must appear in no answer and no log line.
func (s *cpStack) leakCheck(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = append(s.secrets, values...)
}

func (s *cpStack) assertClean(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.secrets {
		if strings.Contains(s.logs.String(), v) {
			t.Errorf("a log line contains the secret %q", v)
		}
		for _, b := range s.bodies {
			if strings.Contains(b, v) {
				t.Errorf("an answer contains the secret %q: %.200s", v, b)
			}
		}
	}
}

func (s *cpStack) do(method, path string, body any, headers ...string) response {
	s.t.Helper()
	resp := s.stack.do(method, path, body, headers...)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(resp.Body))
	s.mu.Unlock()
	return resp
}

func (s *cpStack) get(path string) response { s.t.Helper(); return s.do(http.MethodGet, path, nil) }
func (s *cpStack) post(path string, body any) response {
	s.t.Helper()
	return s.do(http.MethodPost, path, body)
}
func (s *cpStack) put(path string, body any) response {
	s.t.Helper()
	return s.do(http.MethodPut, path, body)
}
func (s *cpStack) del(path string) response { s.t.Helper(); return s.do(http.MethodDelete, path, nil) }

// cpPath is the route of one provider, with the id as the UI sends it (encoded) or raw.
func cpPath(id string, encoded bool) string {
	if encoded {
		return cpProviders + "/" + strings.Replace(id, ":", "%3A", 1)
	}
	return cpProviders + "/" + id
}

// cpInput is a create body (spec §7) with the fields in over replacing or adding to the defaults;
// a nil value removes the field.
func cpInput(id string, over map[string]any) map[string]any {
	in := map[string]any{"id": id, "name": "Synapse", "protocol": "openai", "base_url": "https://synapse.test/v1/"}
	for k, v := range over {
		if v == nil {
			delete(in, k)
			continue
		}
		in[k] = v
	}
	return in
}

// cpUpdate is the PUT body of a provider: the create body without the id.
func cpUpdate(in map[string]any, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if k != "id" {
			out[k] = v
		}
	}
	for k, v := range over {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// cpCreate creates a provider and fails the test unless the answer is 201.
func (s *cpStack) cpCreate(t testing.TB, id string, over map[string]any) map[string]any {
	t.Helper()
	resp := s.post(cpProviders, cpInput(id, over))
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST %s %s = %d %s, want 201", cpProviders, id, resp.Status, resp.Body)
	}
	return resp.Obj(t)
}

// cpIssues lists "instancePath keyword" of the issues of a 422 envelope.
func cpIssues(t testing.TB, resp response) []string {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	details, _ := e["details"].([]any)
	var out []string
	for _, d := range details {
		m, _ := d.(map[string]any)
		out = append(out, fmt.Sprintf("%v %v", m["instancePath"], m["keyword"]))
	}
	return out
}

// cpErr returns error.code and error.message.
func cpErr(t testing.TB, resp response) (code, message string) {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

// secretsFile is the content of secrets.json ("" when the file does not exist).
func (s *cpStack) secretsFile(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "secrets.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (s *cpStack) rows(t testing.TB, table string) int {
	t.Helper()
	return scalar[int](t, s.db.DB, "SELECT count(*) FROM "+table)
}

// cpList returns the provider list as generic objects.
func (s *cpStack) cpList(t testing.TB) []map[string]any {
	t.Helper()
	resp := s.get(cpProviders)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", cpProviders, resp.Status, resp.Body)
	}
	list, ok := resp.JSON(t).([]any)
	if !ok {
		t.Fatalf("GET %s body is not an array: %s", cpProviders, resp.Body)
	}
	out := make([]map[string]any, len(list))
	for i, item := range list {
		out[i], _ = item.(map[string]any)
	}
	return out
}

// cpKeyOrder returns the keys of the first object of a JSON array, in wire order.
func cpKeyOrder(t testing.TB, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		t.Fatalf("body is not an array: %s", body)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("first element is not an object: %s", body)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func cpIDs(list []map[string]any) []string {
	ids := make([]string, len(list))
	for i, p := range list {
		ids[i], _ = p["id"].(string)
	}
	return ids
}

// cpAgent creates an agent through the API and returns its id.
func (s *cpStack) cpAgent(t testing.TB, name, provider string, enabled bool) string {
	t.Helper()
	resp := s.post("/agents", map[string]any{
		"name": name, "provider": provider, "model": "m1", "system_prompt": "Review.", "enabled": enabled,
	})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /agents %s on %s = %d %s, want 201", name, provider, resp.Status, resp.Body)
	}
	id, _ := resp.Obj(t)["id"].(string)
	return id
}

// ---- AC1: the CRUD round trip ----

func TestCustomProviders_CRUDRoundTrip(t *testing.T) { // AC1, FR1, FR2, FR5, EC14
	s := cpBoot(t)
	s.leakCheck(cpKey, cpTenant)

	if got := s.cpList(t); len(got) != 0 {
		t.Fatalf("GET %s on an empty workspace = %v, want []", cpProviders, got)
	}

	created := s.cpCreate(t, "custom:synapse", map[string]any{
		"auth_mode": "header", "auth_header": "api-key", "api_key": cpKey,
		"extra_headers": []any{map[string]any{"name": "X-Tenant", "value": cpTenant}},
		"models": []any{map[string]any{"id": "gpt-x", "label": "GPT X",
			"pricing": map[string]any{"promptPerM": 2.5, "completionPerM": 10}}},
	})
	delete(created, "created_at")
	delete(created, "updated_at")
	want := map[string]any{
		"id": "custom:synapse", "name": "Synapse", "protocol": "openai",
		"base_url": "https://synapse.test/v1", "plain_http": false,
		"auth_mode": "header", "auth_header": "api-key", "structured_output": "strict",
		"key_configured": true,
		"extra_headers":  []any{map[string]any{"name": "X-Tenant", "value_configured": true}},
		"models": []any{map[string]any{"id": "gpt-x", "label": "GPT X",
			"pricing": map[string]any{"promptPerM": 2.5, "completionPerM": float64(10)}}},
	}
	if diff := cmp.Diff(want, created); diff != "" {
		t.Errorf("POST %s answer mismatch (-want +got):\n%s", cpProviders, diff)
	}
	if file := s.secretsFile(t); !strings.Contains(file, cpKey) || !strings.Contains(file, cpTenant) {
		t.Errorf("secrets.json after the create does not hold the key and the header value")
	}

	list := s.cpList(t)
	if diff := cmp.Diff([]string{"custom:synapse"}, cpIDs(list)); diff != "" {
		t.Errorf("GET %s ids mismatch (-want +got):\n%s", cpProviders, diff)
	}
	wantOrder := []string{"id", "name", "protocol", "base_url", "plain_http", "auth_mode", "auth_header", "structured_output",
		"key_configured", "extra_headers", "models", "created_at", "updated_at"}
	if got := cpKeyOrder(t, s.get(cpProviders).Body); !slices.Equal(got, wantOrder) {
		t.Errorf("GET %s key order = %v, want the order of the spec (-want +got):\n%s", cpProviders, got, cmp.Diff(wantOrder, got))
	}

	// PUT renames; the key and the header value stay (the body omits both).
	renamed := s.put(cpPath("custom:synapse", false), cpUpdate(cpInput("custom:synapse", map[string]any{
		"name": "Synapse Prod", "auth_mode": "header", "auth_header": "api-key",
		"extra_headers": []any{map[string]any{"name": "X-Tenant"}},
	}), nil))
	if renamed.Status != http.StatusOK {
		t.Fatalf("PUT %s = %d %s, want 200", cpPath("custom:synapse", false), renamed.Status, renamed.Body)
	}
	if got := renamed.Obj(t); got["name"] != "Synapse Prod" || got["key_configured"] != true {
		t.Errorf("PUT answer name/key_configured = %v/%v, want Synapse Prod/true", got["name"], got["key_configured"])
	}
	if got := s.cpList(t); len(got) != 1 || got[0]["name"] != "Synapse Prod" {
		t.Errorf("GET %s after the rename = %v, want the renamed provider", cpProviders, got)
	}

	del := s.del(cpPath("custom:synapse", false))
	if del.Status != http.StatusOK {
		t.Fatalf("DELETE = %d %s, want 200", del.Status, del.Body)
	}
	if diff := cmp.Diff(map[string]any{"deleted": "custom:synapse"}, del.Obj(t)); diff != "" {
		t.Errorf("DELETE answer mismatch (-want +got):\n%s", diff)
	}
	if got := s.cpList(t); len(got) != 0 {
		t.Errorf("GET %s after the delete = %v, want []", cpProviders, got)
	}
	file := s.secretsFile(t)
	for _, v := range []string{cpKey, cpTenant} {
		if strings.Contains(file, v) {
			t.Errorf("secrets.json after the delete still holds %q", v)
		}
	}
	if n := s.rows(t, "custom_provider_models"); n != 0 {
		t.Errorf("custom_provider_models rows after the delete = %d, want 0", n)
	}
}

func TestCustomProviders_EncodedIDAddressesTheSameProvider(t *testing.T) { // AC1, EC14
	s := cpBoot(t)
	s.cpCreate(t, "custom:enc", nil)

	for _, id := range []string{"custom:enc", "custom%3Aenc"} {
		got := s.put("/settings/custom-providers/"+id, cpUpdate(cpInput("custom:enc", nil), map[string]any{"name": "Via " + id}))
		if got.Status != http.StatusOK || got.Obj(t)["id"] != "custom:enc" || got.Obj(t)["name"] != "Via "+id {
			t.Errorf("PUT /settings/custom-providers/%s = %d %s, want 200 for custom:enc", id, got.Status, got.Body)
		}
	}
	del := s.del(cpPath("custom:enc", true))
	if del.Status != http.StatusOK || del.Obj(t)["deleted"] != "custom:enc" {
		t.Errorf("DELETE with the encoded id = %d %s, want 200 deleted custom:enc", del.Status, del.Body)
	}
	for _, encoded := range []bool{false, true} {
		got := s.put(cpPath("custom:enc", encoded), cpUpdate(cpInput("custom:enc", nil), nil))
		if code, _ := cpErr(t, got); got.Status != http.StatusNotFound || code != "custom_provider_not_found" {
			t.Errorf("PUT after the delete (encoded=%v) = %d %s, want 404 custom_provider_not_found", encoded, got.Status, got.Body)
		}
		got = s.del(cpPath("custom:enc", encoded))
		if code, _ := cpErr(t, got); got.Status != http.StatusNotFound || code != "custom_provider_not_found" {
			t.Errorf("DELETE after the delete (encoded=%v) = %d %s, want 404 custom_provider_not_found", encoded, got.Status, got.Body)
		}
	}
}

// ---- AC2: limits ----

func TestCustomProviders_ValidationRefusesAndStoresNothing(t *testing.T) { // AC2, EC2, EC3, EC11, EC13
	s := cpBoot(t)

	header := func(name string, value any) map[string]any {
		h := map[string]any{"name": name}
		if value != nil {
			h["value"] = value
		}
		return h
	}
	sixHeaders := make([]any, 6)
	for i := range sixHeaders {
		sixHeaders[i] = header(fmt.Sprintf("X-H%d", i), "v")
	}
	manyModels := make([]any, 201)
	for i := range manyModels {
		manyModels[i] = map[string]any{"id": fmt.Sprintf("m%d", i)}
	}
	headerMode := map[string]any{"auth_mode": "header", "auth_header": "api-key"}
	with := func(extra map[string]any, over map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range extra {
			out[k] = v
		}
		for k, v := range over {
			out[k] = v
		}
		return out
	}

	tests := []struct {
		name string
		id   string
		over map[string]any
		want string // "instancePath keyword"
	}{
		{name: "id with an upper-case letter", id: "custom:Syn", want: "/id invalid_string"},
		{name: "id without a slug", id: "custom:", want: "/id invalid_string"},
		{name: "id with a 33 character slug", id: "custom:" + strings.Repeat("a", 33), want: "/id invalid_string"},
		{name: "id without the prefix", id: "synapse", want: "/id invalid_string"},
		{name: "built-in id", id: "openai", want: "/id invalid_string"},
		{name: "id with an underscore", id: "custom:a_b", want: "/id invalid_string"},
		{name: "ftp scheme", id: "custom:a", over: map[string]any{"base_url": "ftp://x"}, want: "/base_url custom"},
		{name: "credentials in the URL", id: "custom:a", over: map[string]any{"base_url": "https://u:p@x/v1"}, want: "/base_url custom"},
		{name: "query in the URL", id: "custom:a", over: map[string]any{"base_url": "https://x/v1?k=1"}, want: "/base_url custom"},
		{name: "fragment in the URL", id: "custom:a", over: map[string]any{"base_url": "https://x/v1#f"}, want: "/base_url custom"},
		{name: "link-local IPv4", id: "custom:a", over: map[string]any{"base_url": "http://169.254.169.254"}, want: "/base_url custom"},
		{name: "link-local IPv6", id: "custom:a", over: map[string]any{"base_url": "http://[fe80::1]"}, want: "/base_url custom"},
		{name: "unspecified IPv4", id: "custom:a", over: map[string]any{"base_url": "http://0.0.0.0"}, want: "/base_url custom"},
		{name: "header content-type", id: "custom:a", over: map[string]any{"extra_headers": []any{header("content-type", "v")}}, want: "/extra_headers/0/name custom"},
		{name: "header Authorization", id: "custom:a", over: map[string]any{"extra_headers": []any{header("Authorization", "v")}}, want: "/extra_headers/0/name custom"},
		{name: "header named like the auth header", id: "custom:a", over: with(headerMode, map[string]any{"extra_headers": []any{header("API-Key", "v")}}), want: "/extra_headers/0/name custom"},
		{name: "duplicate header in another case", id: "custom:a", over: map[string]any{"extra_headers": []any{header("X-Team", "a"), header("x-team", "b")}}, want: "/extra_headers/1/name custom"},
		{name: "sixth header", id: "custom:a", over: map[string]any{"extra_headers": sixHeaders}, want: "/extra_headers too_big"},
		{name: "header value with CR LF", id: "custom:a", over: map[string]any{"extra_headers": []any{header("X-Team", "a\r\nX-Evil: 1")}}, want: "/extra_headers/0/value custom"},
		{name: "201 manual models", id: "custom:a", over: map[string]any{"models": manyModels}, want: "/models too_big"},
		{name: "duplicate model id", id: "custom:a", over: map[string]any{"models": []any{map[string]any{"id": "m"}, map[string]any{"id": "m"}}}, want: "/models/1/id custom"},
		{name: "header mode without a header", id: "custom:a", over: map[string]any{"auth_mode": "header"}, want: "/auth_header invalid_type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(cpProviders, cpInput(tc.id, tc.over))
			if resp.Status != http.StatusUnprocessableEntity {
				t.Fatalf("POST %s = %d %s, want 422", tc.name, resp.Status, resp.Body)
			}
			if code, _ := cpErr(t, resp); code != "validation_error" {
				t.Errorf("POST %s error code = %q, want validation_error", tc.name, code)
			}
			if !slices.Contains(cpIssues(t, resp), tc.want) {
				t.Errorf("POST %s issues = %v, want one %q", tc.name, cpIssues(t, resp), tc.want)
			}
		})
	}
	if n := s.rows(t, "custom_providers"); n != 0 {
		t.Errorf("custom_providers rows after the refused creates = %d, want 0", n)
	}
	if got := s.secretsFile(t); strings.Contains(got, "custom_providers") {
		t.Errorf("secrets.json after the refused creates holds a custom entry: %s", got)
	}
}

func TestCustomProviders_DuplicateAndLimit(t *testing.T) { // AC2, EC1, EC13
	s := cpBoot(t)
	s.cpCreate(t, "custom:one", nil)

	dup := s.post(cpProviders, cpInput("custom:one", map[string]any{"name": "Other"}))
	code, msg := cpErr(t, dup)
	if dup.Status != http.StatusConflict || code != "custom_provider_exists" || msg != "Custom provider custom:one already exists" {
		t.Errorf("POST of an existing id = %d %s, want 409 custom_provider_exists", dup.Status, dup.Body)
	}
	if got := s.cpList(t); len(got) != 1 || got[0]["name"] != "Synapse" {
		t.Errorf("GET after the duplicate = %v, want the first provider unchanged", got)
	}

	for i := 2; i <= 20; i++ {
		s.cpCreate(t, fmt.Sprintf("custom:p%d", i), nil)
	}
	over := s.post(cpProviders, cpInput("custom:p21", nil))
	code, msg = cpErr(t, over)
	if over.Status != http.StatusUnprocessableEntity || code != "custom_provider_limit" || msg != "At most 20 custom providers" {
		t.Errorf("POST of the 21st provider = %d %s, want 422 custom_provider_limit", over.Status, over.Body)
	}
	if n := s.rows(t, "custom_providers"); n != 20 {
		t.Errorf("custom_providers rows = %d, want 20", n)
	}
}

func TestCustomProviders_ConcurrentCreatesOfOneIDAnswerOnceCreated(t *testing.T) { // AC2, EC1
	s := cpBoot(t)
	for round := range 5 {
		id := fmt.Sprintf("custom:race%d", round)
		raw, err := json.Marshal(cpInput(id, nil))
		if err != nil {
			t.Fatal(err)
		}
		statuses := make([]int, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range statuses {
			wg.Go(func() {
				<-start
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+cpProviders, bytes.NewReader(raw))
				if err != nil {
					errs[i] = err
					return
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := s.client.Do(req)
				if err != nil {
					errs[i] = err
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				statuses[i] = resp.StatusCode
			})
		}
		close(start)
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("concurrent POST %s: %v", id, err)
		}
		slices.Sort(statuses)
		if diff := cmp.Diff([]int{http.StatusCreated, http.StatusConflict}, statuses); diff != "" {
			t.Errorf("two concurrent POSTs of %s answered (-want +got):\n%s", id, diff)
		}
	}
	if n := s.rows(t, "custom_providers"); n != 5 {
		t.Errorf("custom_providers rows = %d, want 5", n)
	}
}

// ---- AC42: the structured output mode ----

func TestCustomProviders_StructuredOutputMode(t *testing.T) { // AC42, EC30, FR1
	s := cpBoot(t)

	omitted := s.cpCreate(t, "custom:plain", nil)
	if omitted["structured_output"] != "strict" {
		t.Errorf("structured_output of a provider created without it = %v, want strict", omitted["structured_output"])
	}
	if got := s.cpList(t); got[0]["structured_output"] != "strict" {
		t.Errorf("GET shows structured_output %v, want strict", got[0]["structured_output"])
	}

	anthropic := map[string]any{"protocol": "anthropic", "base_url": "https://anthropic.gw.test"}
	tests := []struct {
		name string
		mode any
		want int
	}{
		{name: "anthropic with json_object", mode: "json_object", want: http.StatusUnprocessableEntity},
		{name: "anthropic with loose", mode: "loose", want: http.StatusUnprocessableEntity},
		{name: "anthropic with prompt_only", mode: "prompt_only", want: http.StatusCreated},
	}
	for i, tc := range tests {
		t.Run("POST "+tc.name, func(t *testing.T) {
			over := map[string]any{"structured_output": tc.mode}
			for k, v := range anthropic {
				over[k] = v
			}
			resp := s.post(cpProviders, cpInput(fmt.Sprintf("custom:anth%d", i), over))
			if resp.Status != tc.want {
				t.Fatalf("POST with structured_output %v = %d %s, want %d", tc.mode, resp.Status, resp.Body, tc.want)
			}
			if tc.want == http.StatusUnprocessableEntity {
				if got := cpIssues(t, resp); !slices.Contains(got, "/structured_output "+map[string]string{"json_object": "custom", "loose": "invalid_enum_value"}[tc.mode.(string)]) {
					t.Errorf("POST with structured_output %v issues = %v, want one at /structured_output", tc.mode, got)
				}
			}
		})
	}

	// The same bodies on PUT of an anthropic provider.
	s.cpCreate(t, "custom:anthput", anthropic)
	for _, mode := range []string{"json_object", "loose"} {
		resp := s.put(cpPath("custom:anthput", false), cpUpdate(cpInput("custom:anthput", anthropic), map[string]any{"structured_output": mode}))
		if resp.Status != http.StatusUnprocessableEntity || !strings.Contains(strings.Join(cpIssues(t, resp), ","), "/structured_output") {
			t.Errorf("PUT with structured_output %q = %d %s, want 422 at /structured_output", mode, resp.Status, resp.Body)
		}
	}
	// An openai provider takes json_object, and PUT without the field resets it to strict.
	s.cpCreate(t, "custom:oai", map[string]any{"structured_output": "json_object"})
	reset := s.put(cpPath("custom:oai", false), cpUpdate(cpInput("custom:oai", nil), nil))
	if reset.Status != http.StatusOK || reset.Obj(t)["structured_output"] != "strict" {
		t.Errorf("PUT without structured_output = %d %s, want 200 with strict", reset.Status, reset.Body)
	}
}

// ---- AC6: the merge of key and header values on update ----

func TestCustomProviders_UpdateKeepsRemovesAndRejectsSecrets(t *testing.T) { // AC6, FR5, EC12
	s := cpBoot(t)
	s.leakCheck(cpKey, cpTenant, cpTeam, "sk-REPLACED-2", "TENANT-REPLACED-4")
	base := cpInput("custom:merge", map[string]any{
		"api_key": cpKey,
		"extra_headers": []any{
			map[string]any{"name": "X-Tenant", "value": cpTenant},
			map[string]any{"name": "X-Team", "value": cpTeam},
		},
	})
	s.cpCreate(t, "custom:merge", base)
	path := cpPath("custom:merge", false)

	headers := func(t testing.TB, p map[string]any) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, h := range p["extra_headers"].([]any) {
			m := h.(map[string]any)
			out[m["name"].(string)] = m["value_configured"].(bool)
		}
		return out
	}

	// 1. No api_key and headers listed without values: everything stays.
	got := s.put(path, cpUpdate(base, map[string]any{
		"api_key":       nil,
		"extra_headers": []any{map[string]any{"name": "X-Tenant"}, map[string]any{"name": "X-Team"}},
	}))
	if got.Status != http.StatusOK {
		t.Fatalf("PUT keeping everything = %d %s, want 200", got.Status, got.Body)
	}
	file := s.secretsFile(t)
	for _, v := range []string{cpKey, cpTenant, cpTeam} {
		if !strings.Contains(file, v) {
			t.Errorf("secrets.json after a PUT that keeps the secrets lacks %q", v)
		}
	}
	if p := got.Obj(t); p["key_configured"] != true || !cpEqual(headers(t, p), map[string]bool{"X-Tenant": true, "X-Team": true}) {
		t.Errorf("PUT keeping everything answered %s, want key_configured and both headers configured", got.Body)
	}

	// 2. A header listed in another case without a value keeps the stored value.
	got = s.put(path, cpUpdate(base, map[string]any{
		"api_key": nil, "extra_headers": []any{map[string]any{"name": "x-tenant"}},
	}))
	if got.Status != http.StatusOK || !cpEqual(headers(t, got.Obj(t)), map[string]bool{"x-tenant": true}) {
		t.Fatalf("PUT with x-tenant and no value = %d %s, want 200 with x-tenant configured", got.Status, got.Body)
	}
	file = s.secretsFile(t)
	if !strings.Contains(file, cpTenant) {
		t.Errorf("secrets.json lost the X-Tenant value on a PUT that listed it in another case")
	}
	if strings.Contains(file, cpTeam) {
		t.Errorf("secrets.json still holds the value of X-Team, which the PUT no longer lists")
	}

	// 3. A new header without a value is refused and nothing changes.
	before := s.secretsFile(t)
	got = s.put(path, cpUpdate(base, map[string]any{
		"api_key": nil, "name": "Must not apply",
		"extra_headers": []any{map[string]any{"name": "x-tenant"}, map[string]any{"name": "X-New"}},
	}))
	if got.Status != http.StatusUnprocessableEntity || !slices.Contains(cpIssues(t, got), "/extra_headers/1/value invalid_type") {
		t.Errorf("PUT with a new header without a value = %d %s, want 422 at /extra_headers/1/value", got.Status, got.Body)
	}
	if after := s.secretsFile(t); after != before {
		t.Errorf("secrets.json changed by a refused PUT")
	}
	if list := s.cpList(t); list[0]["name"] != "Synapse" {
		t.Errorf("name after the refused PUT = %v, want it unchanged", list[0]["name"])
	}

	// 4. api_key null removes the key only.
	got = s.put(path, cpUpdate(base, map[string]any{
		"api_key": nil, "extra_headers": []any{map[string]any{"name": "x-tenant"}},
	}))
	if got.Status != http.StatusOK {
		t.Fatalf("PUT = %d %s", got.Status, got.Body)
	}
	raw := cpUpdate(base, map[string]any{"extra_headers": []any{map[string]any{"name": "x-tenant"}}})
	raw["api_key"] = json.RawMessage("null")
	got = s.put(path, raw)
	if got.Status != http.StatusOK || got.Obj(t)["key_configured"] != false {
		t.Fatalf("PUT with api_key null = %d %s, want key_configured false", got.Status, got.Body)
	}
	file = s.secretsFile(t)
	if strings.Contains(file, cpKey) || !strings.Contains(file, cpTenant) {
		t.Errorf("secrets.json after api_key:null: key present=%v, header value present=%v, want false/true",
			strings.Contains(file, cpKey), strings.Contains(file, cpTenant))
	}
	if list := s.cpList(t); list[0]["key_configured"] != false {
		t.Errorf("GET key_configured after the removal = %v, want false", list[0]["key_configured"])
	}

	// 5. New values replace the old ones.
	got = s.put(path, cpUpdate(base, map[string]any{
		"api_key": "sk-REPLACED-2", "extra_headers": []any{map[string]any{"name": "X-Tenant", "value": "TENANT-REPLACED-4"}},
	}))
	if got.Status != http.StatusOK || got.Obj(t)["key_configured"] != true {
		t.Fatalf("PUT with new values = %d %s, want 200 key_configured", got.Status, got.Body)
	}
	file = s.secretsFile(t)
	if !strings.Contains(file, "sk-REPLACED-2") || !strings.Contains(file, "TENANT-REPLACED-4") || strings.Contains(file, cpTenant) {
		t.Errorf("secrets.json after the replace does not hold exactly the new values")
	}

	// 6. A key that breaks the rules is refused.
	got = s.put(path, cpUpdate(base, map[string]any{"api_key": "bad\nkey"}))
	if got.Status != http.StatusUnprocessableEntity || !slices.Contains(cpIssues(t, got), "/api_key custom") {
		t.Errorf("PUT with a key holding a newline = %d %s, want 422 at /api_key", got.Status, got.Body)
	}

	// 7. A PUT for a missing provider is a 404.
	got = s.put(cpPath("custom:ghost", false), cpUpdate(base, nil))
	if code, _ := cpErr(t, got); got.Status != http.StatusNotFound || code != "custom_provider_not_found" {
		t.Errorf("PUT of an undefined provider = %d %s, want 404 custom_provider_not_found", got.Status, got.Body)
	}
}

func cpEqual[T any](a, b T) bool { return cmp.Equal(a, b) }

// ---- AC7: delete of a provider in use ----

func TestCustomProviders_DeleteInUseAnswers409WithDetails(t *testing.T) { // AC7, FR6, EC7
	s := cpBoot(t)
	s.leakCheck(cpKey)
	s.cpCreate(t, "custom:synapse", map[string]any{"api_key": cpKey})

	agent := s.cpAgent(t, "Disabled on synapse", "custom:synapse", false)
	put := s.put("/settings", map[string]any{
		"default_model":  map[string]any{"provider": "custom:synapse", "model": "m1"},
		"feature_models": map[string]any{"conventions": map[string]any{"provider": "custom:synapse", "model": "m1"}},
	})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT /settings with the defined provider = %d %s, want 200", put.Status, put.Body)
	}

	del := s.del(cpPath("custom:synapse", true))
	code, _ := cpErr(t, del)
	if del.Status != http.StatusConflict || code != "provider_in_use" {
		t.Fatalf("DELETE of a provider in use = %d %s, want 409 provider_in_use", del.Status, del.Body)
	}
	details, _ := del.Obj(t)["error"].(map[string]any)["details"].(map[string]any)
	wantDetails := map[string]any{
		"agents":         []any{map[string]any{"id": agent, "name": "Disabled on synapse"}},
		"feature_models": []any{"conventions"},
		"default_model":  true,
	}
	if diff := cmp.Diff(wantDetails, details); diff != "" {
		t.Errorf("provider_in_use details mismatch (-want +got):\n%s", diff)
	}
	if got := s.cpList(t); len(got) != 1 {
		t.Errorf("providers after the refused delete = %v, want it kept", cpIDs(got))
	}
	if !strings.Contains(s.secretsFile(t), cpKey) {
		t.Errorf("secrets.json after the refused delete lost the key")
	}

	// Free the references one by one: the delete goes through only when none is left.
	if got := s.put("/settings", map[string]any{
		"default_model":  map[string]any{"provider": "openai", "model": "gpt-4.1"},
		"feature_models": map[string]any{"conventions": map[string]any{"provider": "openai", "model": "gpt-4.1"}},
	}); got.Status != http.StatusOK {
		t.Fatalf("PUT /settings freeing the provider = %d %s", got.Status, got.Body)
	}
	if got := s.del(cpPath("custom:synapse", false)); got.Status != http.StatusConflict {
		t.Errorf("DELETE while only the agent uses it = %d %s, want 409", got.Status, got.Body)
	}
	if got := s.del("/agents/" + agent); got.Status != http.StatusOK && got.Status != http.StatusNoContent {
		t.Fatalf("DELETE /agents/%s = %d %s", agent, got.Status, got.Body)
	}
	if got := s.del(cpPath("custom:synapse", false)); got.Status != http.StatusOK {
		t.Errorf("DELETE once nothing uses it = %d %s, want 200", got.Status, got.Body)
	}
}

// ---- AC8: defined and undefined ids in settings and agents ----

func TestCustomProviders_DefinedAndUndefinedIDsInSettingsAndAgents(t *testing.T) { // AC8, FR7
	s := cpBoot(t)
	s.cpCreate(t, "custom:synapse", nil)

	// Defined: stored.
	agent := s.cpAgent(t, "On synapse", "custom:synapse", true)
	if got := s.get("/agents/" + agent); got.Status != http.StatusOK || got.Obj(t)["provider"] != "custom:synapse" {
		t.Errorf("GET of the agent = %d %s, want provider custom:synapse", got.Status, got.Body)
	}
	put := s.put("/settings", map[string]any{
		"default_model":  map[string]any{"provider": "custom:synapse", "model": "m1"},
		"feature_models": map[string]any{"conventions": map[string]any{"provider": "custom:synapse", "model": "m2"}},
	})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT /settings with the defined provider = %d %s, want 200", put.Status, put.Body)
	}
	stored := s.get("/settings").Obj(t)
	if diff := cmp.Diff(map[string]any{"provider": "custom:synapse", "model": "m1"}, stored["default_model"]); diff != "" {
		t.Errorf("stored default_model mismatch (-want +got):\n%s", diff)
	}
	if got := stored["feature_models"].(map[string]any)["conventions"]; !cpEqual(got, any(map[string]any{"provider": "custom:synapse", "model": "m2"})) {
		t.Errorf("stored feature_models.conventions = %v, want custom:synapse/m2", got)
	}

	const ghost = "Custom provider custom:ghost is not configured"
	messages := func(t testing.TB, resp response) []string {
		t.Helper()
		e, _ := resp.Obj(t)["error"].(map[string]any)
		var out []string
		for _, d := range e["details"].([]any) {
			m := d.(map[string]any)
			out = append(out, fmt.Sprintf("%v: %v", m["instancePath"], m["message"]))
		}
		return out
	}
	tests := []struct {
		name string
		do   func() response
		want []string
	}{
		{name: "agent create", do: func() response {
			return s.post("/agents", map[string]any{"name": "Ghost", "provider": "custom:ghost", "model": "m", "system_prompt": "p"})
		}, want: []string{"/provider: " + ghost}},
		{name: "agent update", do: func() response {
			return s.put("/agents/"+agent, map[string]any{"provider": "custom:ghost"})
		}, want: []string{"/provider: " + ghost}},
		{name: "default_model", do: func() response {
			return s.put("/settings", map[string]any{"default_model": map[string]any{"provider": "custom:ghost", "model": "m"}})
		}, want: []string{"/default_model/provider: " + ghost}},
		{name: "feature_models", do: func() response {
			return s.put("/settings", map[string]any{"feature_models": map[string]any{"conventions": map[string]any{"provider": "custom:ghost", "model": "m"}}})
		}, want: []string{"/feature_models/conventions/provider: " + ghost}},
	}
	for _, tc := range tests {
		t.Run(tc.name+" with an undefined id", func(t *testing.T) {
			resp := tc.do()
			if resp.Status != http.StatusUnprocessableEntity {
				t.Fatalf("%s with custom:ghost = %d %s, want 422", tc.name, resp.Status, resp.Body)
			}
			if code, _ := cpErr(t, resp); code != "validation_error" {
				t.Errorf("%s error code = %q, want validation_error", tc.name, code)
			}
			if diff := cmp.Diff(tc.want, messages(t, resp)); diff != "" {
				t.Errorf("%s issues mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM agents WHERE provider = 'custom:ghost'`); n != 0 {
		t.Errorf("agents on custom:ghost = %d, want 0", n)
	}
	if got := s.get("/settings").Obj(t)["default_model"]; !cpEqual(got, any(map[string]any{"provider": "custom:synapse", "model": "m1"})) {
		t.Errorf("default_model after the refused PUTs = %v, want unchanged", got)
	}

	// A malformed id is an invalid value, like any other.
	bad := s.post("/agents", map[string]any{"name": "Bad", "provider": "custom:Bad", "model": "m", "system_prompt": "p"})
	if bad.Status != http.StatusUnprocessableEntity || strings.Contains(string(bad.Body), "is not configured") {
		t.Errorf("POST /agents with custom:Bad = %d %s, want 422 without the not-configured text", bad.Status, bad.Body)
	}
	badPut := s.put("/settings", map[string]any{"default_model": map[string]any{"provider": "custom:Bad", "model": "m"}})
	if badPut.Status != http.StatusUnprocessableEntity || strings.Contains(string(badPut.Body), "is not configured") {
		t.Errorf("PUT /settings with custom:Bad = %d %s, want 422 without the not-configured text", badPut.Status, badPut.Body)
	}
}

// ---- AC10: the model list ----

func TestCustomProviders_ModelsAreTheUnionOfLiveAndManual(t *testing.T) { // AC10, FR10, EC14
	gw := newCPGateway(t, cpListing("b", "a"))
	s := cpBoot(t)
	s.leakCheck(cpKey)
	s.cpCreate(t, "custom:synapse", map[string]any{
		"base_url": gw.base(), "api_key": cpKey,
		"models": []any{
			map[string]any{"id": "a", "label": "A", "pricing": map[string]any{"promptPerM": 1.5, "completionPerM": 6}},
			map[string]any{"id": "c"},
		},
	})

	for _, encoded := range []bool{false, true} {
		gw.reset()
		path := "/providers/custom:synapse/models"
		if encoded {
			path = "/providers/custom%3Asynapse/models"
		}
		resp := s.get(path)
		if resp.Status != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", path, resp.Status, resp.Body)
		}
		var models []map[string]any
		if err := json.Unmarshal(resp.Body, &models); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range models {
			ids = append(ids, m["id"].(string))
			if m["provider"] != "custom:synapse" {
				t.Errorf("GET %s model %v provider = %v, want custom:synapse", path, m["id"], m["provider"])
			}
		}
		if diff := cmp.Diff([]string{"a", "b", "c"}, ids); diff != "" {
			t.Errorf("GET %s ids mismatch (-want +got):\n%s", path, diff)
		}
		if len(models) == 3 {
			if models[0]["label"] != "A" || !cpEqual(models[0]["pricing"], any(map[string]any{"promptPerM": 1.5, "completionPerM": float64(6)})) {
				t.Errorf("GET %s model a = %v, want the manual label and pricing", path, models[0])
			}
			if _, has := models[1]["pricing"]; has {
				t.Errorf("GET %s model b (live only) = %v, want no pricing", path, models[1])
			}
		}
		reqs := gw.requests()
		if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/models" ||
			reqs[0].Header.Get("Authorization") != "Bearer "+cpKey {
			t.Errorf("gateway requests for %s = %+v, want one authorized GET /v1/models", path, reqs)
		}
	}

	manual := []string{"a", "c"}
	failing := []struct {
		name string
		h    http.HandlerFunc
	}{
		{name: "500", h: cpStatus(500, "application/json", `{"error":"boom `+cpKey+`"}`)},
		{name: "404", h: cpStatus(404, "application/json", `{"error":"no such route"}`)},
		{name: "HTML", h: cpStatus(200, "text/html", `<html><body>Sign in</body></html>`)},
	}
	for _, tc := range failing {
		t.Run("gateway answers "+tc.name, func(t *testing.T) {
			gw.set(tc.h)
			resp := s.get("/providers/custom:synapse/models")
			var models []map[string]any
			if err := json.Unmarshal(resp.Body, &models); resp.Status != http.StatusOK || err != nil {
				t.Fatalf("GET models with a %s gateway = %d %s, want 200", tc.name, resp.Status, resp.Body)
			}
			var ids []string
			for _, m := range models {
				ids = append(ids, m["id"].(string))
			}
			if diff := cmp.Diff(manual, ids); diff != "" {
				t.Errorf("GET models with a %s gateway ids mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}

	t.Run("an undefined provider has no models", func(t *testing.T) {
		resp := s.get("/providers/custom:ghost/models")
		if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != "[]" {
			t.Errorf("GET /providers/custom:ghost/models = %d %s, want 200 []", resp.Status, resp.Body)
		}
	})
	t.Run("a malformed id is refused", func(t *testing.T) {
		resp := s.get("/providers/custom:Bad/models")
		if resp.Status != http.StatusUnprocessableEntity {
			t.Errorf("GET /providers/custom:Bad/models = %d %s, want 422", resp.Status, resp.Body)
		}
	})
}

func TestCustomProviders_ModelsFallBackToManualWhenTheGatewayHangs(t *testing.T) { // AC10, EC6
	t.Parallel()
	gw := newCPGateway(t, nil)
	gw.set(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-gw.release:
		}
	})
	s := cpBoot(t)
	s.client = &http.Client{Timeout: 3 * time.Minute} // longer than the 30 s the listing may take
	s.cpCreate(t, "custom:slow", map[string]any{
		"base_url": gw.base(), "models": []any{map[string]any{"id": "a"}, map[string]any{"id": "c"}},
	})

	started := time.Now()
	resp := s.get("/providers/custom:slow/models")
	elapsed := time.Since(started)
	var models []map[string]any
	if err := json.Unmarshal(resp.Body, &models); resp.Status != http.StatusOK || err != nil || len(models) != 2 {
		t.Fatalf("GET models with a hanging gateway = %d %s, want 200 with the two manual models", resp.Status, resp.Body)
	}
	if elapsed < 20*time.Second || elapsed > 60*time.Second {
		t.Errorf("GET models with a hanging gateway took %s, want about the 30 s listing limit", elapsed.Round(time.Second))
	}
}

// ---- AC11: the connection test ----

// cpTest posts a connection test.
func (s *cpStack) cpTest(id, key string) response {
	s.t.Helper()
	body := map[string]any{"provider": id}
	if key != "" {
		body["key"] = key
	}
	return s.post("/settings/test-connection", body)
}

func TestCustomProviders_TestConnectionMakesOneListCallAndNoCompletion(t *testing.T) { // AC11, FR11, EC5, EC6
	gw := newCPGateway(t, cpListing("m1", "m-other"))
	s := cpBoot(t)
	const newKey = "sk-NEW-KEY-3"
	s.leakCheck(newKey, cpKey)
	s.cpCreate(t, "custom:synapse", map[string]any{
		"base_url": gw.base(), "api_key": cpKey,
		"models": []any{map[string]any{"id": "m1"}},
	})

	assertOneList := func(t *testing.T, what, wantAuth string) {
		t.Helper()
		reqs := gw.requests()
		if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/models" {
			t.Fatalf("%s: gateway saw %+v, want exactly one GET /v1/models", what, reqs)
		}
		if got := reqs[0].Header.Get("Authorization"); got != wantAuth {
			t.Errorf("%s: Authorization = %q, want %q", what, got, wantAuth)
		}
		gw.reset()
	}

	t.Run("list 2xx", func(t *testing.T) {
		resp := s.cpTest("custom:synapse", newKey)
		got := resp.Obj(t)
		if resp.Status != http.StatusOK || got["ok"] != true || got["message"] != "OK — 2 models available" ||
			got["model"] != "m1" || got["model_reachable"] != true || got["provider"] != "custom:synapse" {
			t.Fatalf("test-connection = %d %s, want ok with 2 models, model m1 reachable", resp.Status, resp.Body)
		}
		if _, has := got["latency_ms"].(float64); !has {
			t.Errorf("test-connection latency_ms = %v, want a number", got["latency_ms"])
		}
		if w, has := got["warning"]; has && w != nil {
			t.Errorf("test-connection warning = %v, want none", w)
		}
		assertOneList(t, "list 2xx", "Bearer "+newKey)
		if !strings.Contains(s.secretsFile(t), newKey) {
			t.Errorf("the new key was not saved")
		}
	})

	t.Run("model not in the list", func(t *testing.T) {
		gw.set(cpListing("only-other"))
		got := s.cpTest("custom:synapse", "").Obj(t)
		if got["ok"] != true || got["model"] != "m1" || got["model_reachable"] != false {
			t.Errorf("test-connection with the model missing = %v, want ok, model m1, model_reachable false", got)
		}
		assertOneList(t, "model not in the list", "Bearer "+newKey)
	})

	for _, status := range []int{404, 405} {
		t.Run(fmt.Sprintf("list %d is a warning", status), func(t *testing.T) {
			gw.set(cpStatus(status, "application/json", `{}`))
			resp := s.cpTest("custom:synapse", "")
			got := resp.Obj(t)
			if resp.Status != http.StatusOK || got["ok"] != true || got["warning"] != "Model list not available; key not verified" {
				t.Fatalf("test-connection with a %d list = %d %s, want ok:true and the warning", status, resp.Status, resp.Body)
			}
			if reachable, has := got["model_reachable"]; has && reachable != nil {
				t.Errorf("model_reachable = %v, want null", reachable)
			}
			assertOneList(t, fmt.Sprintf("list %d", status), "Bearer "+newKey)
		})
	}

	failing := []struct {
		name string
		h    http.HandlerFunc
	}{
		{name: "401 echoing the key", h: cpStatus(401, "application/json", `{"error":{"message":"Incorrect API key provided: `+newKey+`"}}`)},
		{name: "500", h: cpStatus(500, "text/plain", "upstream broke")},
		{name: "HTML", h: cpStatus(200, "text/html", "<html>Login</html>")},
	}
	for _, tc := range failing {
		t.Run("list "+tc.name+" fails", func(t *testing.T) {
			gw.set(tc.h)
			resp := s.cpTest("custom:synapse", "sk-ANOTHER-KEY-5")
			got := resp.Obj(t)
			if resp.Status != http.StatusOK || got["ok"] != false || got["message"] == "" {
				t.Errorf("test-connection with a %s list = %d %s, want ok:false with a message", tc.name, resp.Status, resp.Body)
			}
			if !strings.Contains(s.secretsFile(t), "sk-ANOTHER-KEY-5") {
				t.Errorf("the key was not saved after a failed test (%s)", tc.name)
			}
			assertOneList(t, tc.name, "Bearer sk-ANOTHER-KEY-5")
		})
	}
	s.leakCheck("sk-ANOTHER-KEY-5")

	t.Run("an undefined provider is a 404 and saves nothing", func(t *testing.T) {
		before := s.secretsFile(t)
		resp := s.cpTest("custom:ghost", "sk-GHOST-KEY-6")
		if code, _ := cpErr(t, resp); resp.Status != http.StatusNotFound || code != "custom_provider_not_found" {
			t.Errorf("test-connection of custom:ghost = %d %s, want 404 custom_provider_not_found", resp.Status, resp.Body)
		}
		if after := s.secretsFile(t); after != before || strings.Contains(after, "sk-GHOST-KEY-6") {
			t.Errorf("secrets.json changed by the test of an undefined provider")
		}
		if reqs := gw.requests(); len(reqs) != 0 {
			t.Errorf("gateway saw %+v for an undefined provider, want nothing", reqs)
		}
	})
}

// cpDNS answers every A query with ip and every other query with an empty answer, and makes the
// process resolve through it until the test ends. It plays "a host name that resolves to a
// link-local address at call time" without the network.
func cpDNS(t *testing.T, ip [4]byte) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			q := buf[:n]
			end := 12
			for end < n && q[end] != 0 {
				end += int(q[end]) + 1
			}
			end += 5 // the root label, QTYPE and QCLASS
			if end > n {
				continue
			}
			isA := q[end-4] == 0 && q[end-3] == 1
			resp := append([]byte{q[0], q[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}, q[12:end]...)
			if isA {
				resp[7] = 1
				resp = append(resp, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, ip[0], ip[1], ip[2], ip[3])
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
		},
	}
	t.Cleanup(func() {
		net.DefaultResolver = previous
		_ = pc.Close()
		<-done
	})
}

func TestCustomProviders_TestConnectionRefusesAForbiddenAddress(t *testing.T) { // EC3, FR3
	cpDNS(t, [4]byte{169, 254, 169, 254})
	s := cpBoot(t)
	s.leakCheck("sk-META-KEY-8")
	// The create route accepts the host name: only the address it resolves to at call time is bad.
	s.cpCreate(t, "custom:meta", map[string]any{
		"base_url": "http://metadata.gateway.test/v1", "api_key": "sk-META-KEY-8",
		"models": []any{map[string]any{"id": "m1"}},
	})

	resp := s.cpTest("custom:meta", "")
	got := resp.Obj(t)
	if resp.Status != http.StatusOK || got["ok"] != false || got["message"] != "Base URL resolves to a forbidden address" {
		t.Errorf("test-connection to a host resolving to 169.254.169.254 = %d %s, want ok:false \"Base URL resolves to a forbidden address\"", resp.Status, resp.Body)
	}
	models := s.get("/providers/custom:meta/models")
	var list []map[string]any
	if err := json.Unmarshal(models.Body, &list); models.Status != http.StatusOK || err != nil || len(list) != 1 || list[0]["id"] != "m1" {
		t.Errorf("GET models of a forbidden gateway = %d %s, want 200 with the manual model only", models.Status, models.Body)
	}

	// A literal link-local address is refused before any dial; nothing can store one through the API.
	e := s.env
	e.exec(t, `INSERT INTO custom_providers (workspace_id, id, name, protocol, base_url, auth_mode, extra_header_names)
		VALUES (?, 'literal', 'Literal', 'openai', 'http://169.254.169.254/v1', 'bearer', '{}')`, e.ws)
	lit := s.cpTest("custom:literal", "")
	if got := lit.Obj(t); lit.Status != http.StatusOK || got["ok"] != false {
		t.Errorf("test-connection to a link-local literal = %d %s, want ok:false", lit.Status, lit.Body)
	}
}

// ---- AC14: the extra CA ----

func TestCustomProviders_ExtraCAFileTrustsAPrivateGateway(t *testing.T) { // AC14, FR14, EC27
	gw := newCPTLSGateway(t, cpListing("m1"))
	caFile := filepath.Join(t.TempDir(), "gateway-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gw.srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	e := newSBEnv(t)
	with := cpBootOn(t, e, func(sp *spec) { sp.config = func(c *config.Config) { c.ExtraCAFile = caFile } })
	with.cpCreate(t, "custom:private", map[string]any{"base_url": gw.base(), "models": []any{map[string]any{"id": "m1"}}})
	got := with.cpTest("custom:private", "sk-CA-KEY-9").Obj(t)
	with.leakCheck("sk-CA-KEY-9")
	if got["ok"] != true || got["message"] != "OK — 1 models available" {
		t.Errorf("test-connection with DEVDIGEST_EXTRA_CA_FILE = %v, want ok:true", got)
	}
	if models := with.get("/providers/custom:private/models"); models.Status != http.StatusOK || !strings.Contains(string(models.Body), `"m1"`) {
		t.Errorf("GET models with the extra CA = %d %s, want the live model", models.Status, models.Body)
	}
	if err := with.stop(); err != nil {
		t.Fatalf("stop the server with the extra CA: %v", err)
	}

	// The same database and gateway without the variable: the certificate is unknown.
	gw.reset()
	without := cpBootOn(t, e)
	resp := without.cpTest("custom:private", "sk-CA-KEY-9")
	without.leakCheck("sk-CA-KEY-9")
	if got := resp.Obj(t); resp.Status != http.StatusOK || got["ok"] != false || got["message"] == "" {
		t.Errorf("test-connection without DEVDIGEST_EXTRA_CA_FILE = %d %s, want ok:false", resp.Status, resp.Body)
	}
	if reqs := gw.requests(); len(reqs) != 0 {
		t.Errorf("the gateway saw %d request(s) although the handshake must fail", len(reqs))
	}
}

// ---- AC16: the guards ----

func TestCustomProviders_RoutesKeepTheGuards(t *testing.T) { // AC16, NFR3
	s := cpBoot(t)
	s.cpCreate(t, "custom:guarded", nil)
	body := cpInput("custom:guard2", nil)
	update := cpUpdate(cpInput("custom:guarded", nil), map[string]any{"name": "Changed"})
	one := cpPath("custom:guarded", false)

	routes := []struct {
		name, method, path string
		body               any
		state              bool // state-changing
	}{
		{name: "GET list", method: http.MethodGet, path: cpProviders},
		{name: "POST", method: http.MethodPost, path: cpProviders, body: body, state: true},
		{name: "PUT", method: http.MethodPut, path: one, body: update, state: true},
		{name: "DELETE", method: http.MethodDelete, path: one, state: true},
	}
	for _, rt := range routes {
		t.Run(rt.name+" with a foreign Host", func(t *testing.T) {
			resp := s.do(rt.method, rt.path, rt.body, "Host", "evil.example")
			want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
			if resp.Status != http.StatusForbidden {
				t.Fatalf("%s %s with a foreign Host = %d %s, want 403", rt.method, rt.path, resp.Status, resp.Body)
			}
			if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
				t.Errorf("%s foreign Host body mismatch (-want +got):\n%s", rt.name, diff)
			}
		})
		if !rt.state {
			continue
		}
		t.Run(rt.name+" with a foreign Origin", func(t *testing.T) {
			resp := s.do(rt.method, rt.path, rt.body, "Origin", cpEvil)
			if code, _ := cpErr(t, resp); resp.Status != http.StatusForbidden || code != "forbidden" {
				t.Errorf("%s %s with a foreign Origin = %d %s, want 403 forbidden", rt.method, rt.path, resp.Status, resp.Body)
			}
		})
	}

	// D31: text/plain is read like the other routes (a JSON string, so a validation failure: 422);
	// every other non-JSON media type is 415.
	for _, rt := range routes {
		if rt.body == nil {
			continue
		}
		raw, err := json.Marshal(rt.body)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(rt.name+" as text/plain", func(t *testing.T) {
			resp := s.do(rt.method, rt.path, string(raw), "Content-Type", "text/plain")
			if resp.Status != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s as text/plain = %d %s, want 422", rt.method, rt.path, resp.Status, resp.Body)
			}
			if code, _ := cpErr(t, resp); code != "validation_error" {
				t.Errorf("%s as text/plain error code = %q, want validation_error", rt.name, code)
			}
		})
		for _, media := range []string{"application/xml", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
			t.Run(rt.name+" as "+media, func(t *testing.T) {
				// The refusal is the one of the existing routes: status, code and message.
				other := s.do(http.MethodPut, "/workspace", `{"name":"Kept"}`, "Content-Type", media)
				resp := s.do(rt.method, rt.path, string(raw), "Content-Type", media)
				otherCode, otherMsg := cpErr(t, other)
				code, msg := cpErr(t, resp)
				if resp.Status != http.StatusUnsupportedMediaType || other.Status != http.StatusUnsupportedMediaType {
					t.Fatalf("%s %s as %s = %d %s, want 415 (PUT /workspace answers %d)", rt.method, rt.path, media, resp.Status, resp.Body, other.Status)
				}
				if code != otherCode || msg != otherMsg {
					t.Errorf("%s %s as %s error = %s/%s, want the existing routes' %s/%s", rt.method, rt.path, media, code, msg, otherCode, otherMsg)
				}
			})
		}
	}

	list := s.cpList(t)
	if diff := cmp.Diff([]string{"custom:guarded"}, cpIDs(list)); diff != "" || list[0]["name"] != "Synapse" {
		t.Errorf("providers after the refused requests = %v, want the one untouched provider (-want +got):\n%s", list, diff)
	}
}

// ---- no secret in an answer or a log line ----

func TestCustomProviders_NoSecretInAnAnswerOrALog(t *testing.T) { // NFR1, AC5 (answers), AC16
	const echoed = "Incorrect API key provided: " + cpKey + " tenant " + cpTenant
	gw := newCPGateway(t, cpStatus(401, "application/json", `{"error":{"message":"`+echoed+`"}}`))
	s := cpBoot(t)
	s.leakCheck(cpKey, cpTenant, cpTeam, "sk-SYNAP**********ET-1")
	s.cpCreate(t, "custom:leaky", map[string]any{
		"base_url": gw.base(), "api_key": cpKey, "models": []any{map[string]any{"id": "a"}},
		"extra_headers": []any{
			map[string]any{"name": "X-Tenant", "value": cpTenant}, map[string]any{"name": "X-Team", "value": cpTeam},
		},
	})

	for _, resp := range []response{
		s.cpTest("custom:leaky", ""),
		s.cpTest("custom:leaky", cpKey),
		s.get("/providers/custom:leaky/models"),
		s.get(cpProviders),
		s.put(cpPath("custom:leaky", false), cpUpdate(cpInput("custom:leaky", map[string]any{"base_url": gw.base()}), map[string]any{
			"extra_headers": []any{map[string]any{"name": "X-Tenant"}, map[string]any{"name": "X-Team"}},
		})),
		s.post(cpProviders, cpInput("custom:leaky", map[string]any{"api_key": cpKey})), // 409
		s.post(cpProviders, cpInput("custom:Bad", map[string]any{"api_key": cpKey, "extra_headers": []any{map[string]any{"name": "Authorization", "value": cpTenant}}})),
	} {
		if resp.Status >= 500 {
			t.Errorf("an answer was %d %s, want no 5xx", resp.Status, resp.Body)
		}
	}
	if len(gw.requests()) == 0 {
		t.Fatal("the gateway was never called; the leak scenario did not run")
	}
	// The registered secrets are scanned in every answer and in the logs when the test ends.
}

// ---- the contract goldens (NFR11) ----

// TestContracts_CustomProviders records the answers the UI contract test validates (the strict
// schemas are in frontend/src/test/go-backend-contracts.test.ts). The clock is fixed; the gateway
// is only called for the model list and the connection tests, whose answers hold no URL.
func TestContracts_CustomProviders(t *testing.T) {
	gw := newCPGateway(t, cpListing("m1", "m-live"))
	s := cpBoot(t)
	g := sbGoldens(t, s.stack)

	create := cpInput("custom:synapse", map[string]any{
		"auth_mode": "header", "auth_header": "api-key", "api_key": cpKey,
		"extra_headers": []any{map[string]any{"name": "X-Tenant", "value": cpTenant}},
		"models": []any{
			map[string]any{"id": "gpt-x", "label": "GPT X", "pricing": map[string]any{"promptPerM": 2.5, "completionPerM": 10}},
			map[string]any{"id": "plain"},
		},
	})
	g.check("custom_providers_create", s.post(cpProviders, create), http.StatusCreated)
	g.check("custom_providers_update", s.put(cpPath("custom:synapse", true), cpUpdate(create, map[string]any{
		"name": "Synapse Prod", "extra_headers": []any{map[string]any{"name": "X-Tenant"}},
	})), http.StatusOK)
	g.check("custom_providers_create_anthropic", s.post(cpProviders, cpInput("custom:claude", map[string]any{
		"name": "Claude gateway", "protocol": "anthropic", "base_url": "http://claude.gw.corp/", "structured_output": "prompt_only",
	})), http.StatusCreated)
	g.check("custom_providers_list", s.get(cpProviders), http.StatusOK)
	g.check("custom_providers_invalid", s.post(cpProviders, cpInput("custom:Bad", map[string]any{
		"base_url": "http://169.254.169.254", "structured_output": "loose",
	})), http.StatusUnprocessableEntity)

	s.cpCreate(t, "custom:live", map[string]any{
		"base_url": gw.base(), "models": []any{map[string]any{"id": "m1", "label": "Model one", "pricing": map[string]any{"promptPerM": 1, "completionPerM": 4}}},
	})
	g.check("custom_providers_models", s.get("/providers/custom%3Alive/models"), http.StatusOK)
	g.check("custom_providers_test_ok", s.cpTest("custom:live", "sk-GOLDEN-1"), http.StatusOK)
	gw.set(cpStatus(404, "application/json", `{}`))
	g.check("custom_providers_test_warning", s.cpTest("custom:live", ""), http.StatusOK)

	s.cpAgent(t, "Reviewer on synapse", "custom:synapse", false)
	if put := s.put("/settings", map[string]any{
		"default_model":  map[string]any{"provider": "custom:synapse", "model": "gpt-x"},
		"feature_models": map[string]any{"conventions": map[string]any{"provider": "custom:synapse", "model": "gpt-x"}},
	}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings = %d %s, want 200", put.Status, put.Body)
	}
	g.check("custom_providers_in_use", s.del(cpPath("custom:synapse", false)), http.StatusConflict)
	g.check("custom_providers_deleted", s.del(cpPath("custom:claude", true)), http.StatusOK)
}
