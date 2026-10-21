# Test patterns

## Contents
1. Table-driven subtests with cmp.Diff
2. Helper with t.Helper and t.Cleanup
3. Fake for a port
4. Handler test through the real mux
5. HTTP client against httptest.Server
6. Time with synctest
7. Fuzz test
8. Golden file
9. Benchmark
10. Errors from goroutines

## 1. Table-driven subtests with cmp.Diff

```go
func TestParseAmount(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    domain.Money
		wantErr error
	}{
		{name: "whole euros", in: "12 EUR", want: domain.Money{Cents: 1200, Currency: "EUR"}},
		{name: "empty", in: "", wantErr: domain.ErrInvalidAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseAmount(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ParseAmount(%q) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseAmount(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}
```

## 2. Helper with t.Helper and t.Cleanup

```go
func newTestService(t *testing.T) (*app.Service, *orderstest.FakeStore) {
	t.Helper()
	store := orderstest.NewFakeStore()
	log := slog.New(slog.NewTextHandler(t.Output(), nil)) // Go 1.25+; else io.Discard
	return app.NewService(store, log), store
}
```

Setup that can fail calls `t.Fatalf` inside the helper; teardown goes to `t.Cleanup`.

## 3. Fake for a port

```go
package orderstest // internal/orders/orderstest

type FakeStore struct {
	mu     sync.Mutex
	orders map[domain.OrderID]domain.Order
}

func NewFakeStore(seed ...domain.Order) *FakeStore {
	f := &FakeStore{orders: map[domain.OrderID]domain.Order{}}
	for _, o := range seed {
		f.orders[o.ID] = o
	}
	return f
}

func (f *FakeStore) UpdateByID(ctx context.Context, id domain.OrderID, fn func(*domain.Order) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[id]
	if !ok {
		return domain.ErrNotFound
	}
	if err := fn(&o); err != nil {
		return err // like a rollback: nothing stored
	}
	f.orders[id] = o
	return nil
}
```

Run the repository contract suite (references/integration.md) against the fake too, so
it cannot drift from the real implementation.

## 4. Handler test through the real mux

```go
func TestCancelOrder_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	mux := http.NewServeMux()
	httpapi.AddRoutes(mux, svc, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/orders/missing/cancel", strings.NewReader(`{"reason":"x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /orders/missing/cancel status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
```

Calling a handler directly (without the mux) needs `req.SetPathValue("id", "missing")`.

## 5. HTTP client against httptest.Server

```go
func TestRatesClient_Get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rates/EUR" {
			t.Errorf("path = %q, want /rates/EUR", r.URL.Path) // Error, not Fatal: server goroutine
		}
		fmt.Fprint(w, `{"rate": 1.08}`)
	}))
	t.Cleanup(srv.Close)

	c := rates.NewClient(srv.URL, srv.Client())
	got, err := c.Get(t.Context(), "EUR")
	if err != nil {
		t.Fatalf("Get(EUR) error = %v", err)
	}
	if got != 1.08 {
		t.Errorf("Get(EUR) = %v, want 1.08", got)
	}
}
```

## 6. Time with synctest

```go
func TestRetry_GivesUpAfterTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		err := worker.RetryUntil(ctx, func() error { return errTemporary }) // sleeps between attempts
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RetryUntil() error = %v, want DeadlineExceeded", err)
		}
	})
}
```

Inside the bubble `time.Sleep`, timers and `context.WithTimeout` use a fake clock that
advances when every goroutine is blocked; the test runs instantly. Call `synctest.Wait()`
to let background goroutines settle before asserting on their effects. Real network I/O
does not count as "durably blocked" — use in-memory pipes or `httptest.NewTestServer`
(Go 1.27+).

## 7. Fuzz test

```go
func FuzzParseAmount(f *testing.F) {
	f.Add("12 EUR")
	f.Add("0.5 USD")
	f.Fuzz(func(t *testing.T, s string) {
		m, err := domain.ParseAmount(s)
		if err != nil {
			return
		}
		back, err := domain.ParseAmount(m.String())
		if err != nil || back != m {
			t.Errorf("ParseAmount(%q) round trip = %v, %v; want %v", s, back, err, m)
		}
	})
}
```

Assert a property (round trip, no panic, invariant holds), not specific outputs.

## 8. Golden file

```go
var update = flag.Bool("update", false, "rewrite golden files")

func TestRenderInvoice(t *testing.T) {
	got := invoice.Render(sampleOrder())
	path := filepath.Join("testdata", t.Name()+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("Render() mismatch (-want +got):\n%s", diff)
	}
}
```

`go test ./pkg -run TestRenderInvoice -update`, then review the golden diff like code.

## 9. Benchmark

```go
func BenchmarkTotal(b *testing.B) {
	o := largeOrder() // setup runs once
	for b.Loop() {
		o.Total()
	}
}
```

## 10. Errors from goroutines

```go
errc := make(chan error, n)
for i := range n {
	go func() { errc <- doWork(t.Context(), i) }()
}
for range n {
	if err := <-errc; err != nil {
		t.Errorf("doWork() error = %v", err)
	}
}
```
