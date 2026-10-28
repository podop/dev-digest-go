package domain

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestExtractFacts_Go(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		endpoints []string
		crons     []string
	}{
		{"mux method pattern", `mux.HandleFunc("GET /users/{id}", h)`, []string{"GET /users/{id}"}, []string{}},
		{"mux host dropped", `mux.Handle("POST example.com/items", h)`, []string{"POST /items"}, []string{}},
		{"http without method", `http.HandleFunc("/health", h)`, []string{"ANY /health"}, []string{}},
		{"echo verb", `e.POST("/items", h.Create)`, []string{"POST /items"}, []string{}},
		{"gin any", `g.Any("/w", h)`, []string{"ANY /w"}, []string{}},
		{"grpc", `pb.RegisterBillingServiceServer(s, srv)`, []string{"GRPC BillingService"}, []string{}},
		{"grpc unqualified", `RegisterFooServer(s, srv)`, []string{"GRPC Foo"}, []string{}},
		{"cron addfunc", `c.AddFunc("@every 5m", f)`, []string{}, []string{"@every 5m"}},
		{"cron addjob", `c.AddJob("*/5 * * * *", j)`, []string{}, []string{"*/5 * * * *"}},
		{"duplicates", "e.GET(\"/a\", h)\ne.GET(\"/a\", h)\ne.POST(\"/b\", h)", []string{"GET /a", "POST /b"}, []string{}},
		{"neg http.Get", `http.Get("https://x")`, []string{}, []string{}},
		{"neg client.Post", `client.Post("/x", b)`, []string{}, []string{}},
		{"neg title case", `r.Get("/x", h)`, []string{}, []string{}},
		{"neg variable path", `e.GET(path, h)`, []string{}, []string{}},
		{"neg path without slash", `e.GET("x", h)`, []string{}, []string{}},
		{"neg lower verb", `e.get("/x", h)`, []string{}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ExtractFacts("svc/api.go", tc.in)
			wantOK := len(tc.endpoints)+len(tc.crons) > 0
			if ok != wantOK {
				t.Fatalf("ExtractFacts(%q) ok = %v, want %v", tc.in, ok, wantOK)
			}
			if !ok {
				return
			}
			want := FileFacts{Path: "svc/api.go", Endpoints: tc.endpoints, Crons: tc.crons}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ExtractFacts(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestExtractFacts_GoIgnoresTSPatterns(t *testing.T) {
	if got, ok := ExtractFacts("a.go", "app.get('/x', h)\nschedule('* * * * *')"); ok {
		t.Errorf("ExtractFacts(.go with TS patterns) = %+v, want no facts", got)
	}
}

func TestExtractFacts_TSUnchanged(t *testing.T) {
	for _, p := range []string{"a.ts", "a.tsx", "a.js", "a.mjs"} {
		got, ok := ExtractFacts(p, "app.get('/a')\nschedule('* * * * *')")
		want := FileFacts{Path: p, Endpoints: []string{"GET /a"}, Crons: []string{"* * * * *"}}
		if !ok || cmp.Diff(want, got) != "" {
			t.Errorf("ExtractFacts(%q) = %+v, %v; want %+v", p, got, ok, want)
		}
	}
	if _, ok := ExtractFacts("a.ts", `e.GET("/x", h)`); ok {
		t.Error("ExtractFacts(.ts with Go patterns) ok = true, want false")
	}
}
