# Patterns

## Contents
1. Consumer-side port and constructor
2. Transaction with UpdateFn
3. Domain errors and the HTTP edge
4. Handler and routes
5. main → run with graceful shutdown
6. Config

Package paths below use `example.com/shop`; replace them with the module path from `go.mod`.

## 1. Consumer-side port and constructor

```go
package app

// orderStore is what the use cases need — declared here, implemented in adapters/postgres.
type orderStore interface {
	Get(ctx context.Context, id domain.OrderID) (domain.Order, error)
	UpdateByID(ctx context.Context, id domain.OrderID, fn func(*domain.Order) error) error
}

type Service struct {
	orders orderStore
	log    *slog.Logger
}

func NewService(orders orderStore, log *slog.Logger) *Service {
	if orders == nil || log == nil {
		panic("app.NewService: nil dependency") // startup only
	}
	return &Service{orders: orders, log: log}
}
```

The adapter asserts it satisfies the port at compile time only if the port is exported;
otherwise `main` wiring is the check.

## 2. Transaction with UpdateFn

```go
// app: the business decision runs inside the closure.
func (s *Service) Cancel(ctx context.Context, id domain.OrderID, reason string) error {
	return s.orders.UpdateByID(ctx, id, func(o *domain.Order) error {
		return o.Cancel(reason)
	})
}
```

```go
// adapters/postgres: transaction and locking, no business rules.
func (r *OrderRepo) UpdateByID(ctx context.Context, id domain.OrderID, fn func(*domain.Order) error) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	o, err := r.getForUpdate(ctx, tx, id) // SELECT … FOR UPDATE, maps sql.ErrNoRows → domain.ErrNotFound
	if err != nil {
		return err
	}
	if err = fn(&o); err != nil {
		return err
	}
	if err = r.save(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit()
}
```

## 3. Domain errors and the HTTP edge

```go
package domain

var ErrNotFound = errors.New("order not found")

type ValidationError struct{ Field, Reason string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }
```

```go
package http // transport

func statusFor(err error) int {
	var ve *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound
	case errors.As(err, &ve):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
```

Log 5xx at the edge with request context; return a generic body. Never send `err.Error()`
of an internal error to the client.

## 4. Handler and routes

```go
func AddRoutes(mux *http.ServeMux, orders *app.Service, log *slog.Logger) {
	mux.Handle("POST /orders/{id}/cancel", handleCancel(orders, log))
}

func handleCancel(orders *app.Service, log *slog.Logger) http.Handler {
	type request struct{ Reason string `json:"reason"` }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := orders.Cancel(r.Context(), domain.OrderID(r.PathValue("id")), req.Reason); err != nil {
			code := statusFor(err)
			if code >= 500 {
				log.ErrorContext(r.Context(), "cancel order", "err", err)
			}
			http.Error(w, http.StatusText(code), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

DTO types are local to the handler (or the transport package) and mapped explicitly.

## 5. main → run with graceful shutdown

```go
func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(args, getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	db, err := pgpool.Open(ctx, cfg.DatabaseURL) // internal/platform/postgres
	if err != nil {
		return err
	}
	defer db.Close()

	mux := http.NewServeMux()
	orders := app.NewService(pgorders.NewOrderRepo(db), log) // internal/orders/adapters/postgres
	httpapi.AddRoutes(mux, orders, log)                      // internal/orders/transport/http
	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
```

`run` is testable: tests pass their own args, `getenv` and writer.

## 6. Config

```go
type Config struct {
	Addr        string
	DatabaseURL string
}

func loadConfig(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	cfg := Config{DatabaseURL: getenv("DATABASE_URL")}
	fs.StringVar(&cfg.Addr, "addr", ":8080", "listen address")
	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}
```

Use a local `FlagSet`, never the global `flag` package outside `main`; secrets come from
the environment or a secrets provider, never from flags or committed files.
