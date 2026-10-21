// Command warrant runs the Warrant API.
//
//	warrant serve [-migrate]   run the HTTP API (and the hold sweeper)
//	warrant migrate            apply database migrations
//	warrant keygen             print a new WARRANT_SIGNING_KEY
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/internal/config"
	"github.com/okemwag/warrant/server/api"
	"github.com/okemwag/warrant/server/checkout"
	"github.com/okemwag/warrant/server/compiler"
	"github.com/okemwag/warrant/server/db"
	"github.com/okemwag/warrant/server/ledger"
	"github.com/okemwag/warrant/server/merchants"
	"github.com/okemwag/warrant/server/signer"
)

func main() {
	_ = config.LoadDotEnv(".env")
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)

	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		migrate := fs.Bool("migrate", false, "apply migrations before serving")
		_ = fs.Parse(os.Args[2:])
		err = serve(log, *migrate)
	case "migrate":
		err = migrate(log)
	case "keygen":
		fmt.Println("WARRANT_SIGNING_KEY=" + signer.NewSeed())
	default:
		usage()
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: warrant serve [-migrate] | migrate | keygen")
	os.Exit(2)
}

func migrate(log *slog.Logger) error {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"), "")
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	log.Info("migrations", "applied", applied)
	return err
}

func serve(log *slog.Logger, runMigrations bool) error {
	// Rule 7: refuse to start against live PayPal.
	if u := strings.TrimRight(os.Getenv("PAYPAL_BASE_URL"), "/"); u != "" && u != paypal.SandboxBaseURL {
		return paypal.ErrLiveURL
	}
	ttl, err := time.ParseDuration(config.Get("WARRANT_RESERVATION_TTL", "15m"))
	if err != nil {
		return fmt.Errorf("WARRANT_RESERVATION_TTL: %w", err)
	}
	sig, err := signer.FromBase64Seed(os.Getenv("WARRANT_SIGNING_KEY"))
	if err != nil {
		return err
	}
	for _, k := range []string{"WARRANT_CONSOLE_TOKEN", "WARRANT_AGENT_TOKEN"} {
		if strings.Contains(os.Getenv(k), "change-me") {
			log.Warn("using the example token; set a long random value before deploying", "var", k)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"), "")
	if err != nil {
		return err
	}
	defer pool.Close()
	if runMigrations {
		applied, err := db.Migrate(ctx, pool)
		if err != nil {
			return err
		}
		log.Info("migrations", "applied", applied)
	}

	l := ledger.New(pool, sig, ledger.Config{ReservationTTL: ttl})
	cfg := api.Config{
		ConsoleToken: os.Getenv("WARRANT_CONSOLE_TOKEN"),
		AgentToken:   os.Getenv("WARRANT_AGENT_TOKEN"),
		Logger:       log,
		Merchants:    merchants.Default(),
		ConsoleURL:   config.Get("WARRANT_CONSOLE_URL", "/"),
	}

	// PayPal (Phase 3). Without credentials, payment endpoints answer 503.
	if id, secret := os.Getenv("PAYPAL_CLIENT_ID"), os.Getenv("PAYPAL_CLIENT_SECRET"); id != "" && secret != "" {
		pp, err := paypal.New(paypal.Config{BaseURL: os.Getenv("PAYPAL_BASE_URL"), ClientID: id, ClientSecret: secret})
		if err != nil {
			return err
		}
		tol, err := strconv.ParseInt(config.Get("WARRANT_PRICE_TOLERANCE_CENTS", "0"), 10, 64)
		if err != nil || tol < 0 {
			return fmt.Errorf("WARRANT_PRICE_TOLERANCE_CENTS must be a non-negative integer")
		}
		cfg.Checkout = checkout.New(pool, l, pp, checkout.Config{
			Mode:      config.Get("PAYPAL_MODE", checkout.ModeApprove),
			Tolerance: engine.Cents(tol),
			PublicURL: config.Get("WARRANT_PUBLIC_URL", "http://localhost:8080"),
			WebhookID: os.Getenv("PAYPAL_WEBHOOK_ID"),
			Logger:    log,
		})
		if os.Getenv("PAYPAL_WEBHOOK_ID") == "" {
			log.Warn("PAYPAL_WEBHOOK_ID not set: webhooks are ignored; checkout still completes by asking PayPal directly")
		}
		log.Info("paypal sandbox enabled", "mode", cfg.Checkout.Mode())
	} else {
		log.Warn("PAYPAL_CLIENT_ID/SECRET not set: checkout disabled")
	}

	// Claude compiler (Phase 4).
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.Compiler = compiler.New(compiler.NewClaude(key, os.Getenv("WARRANT_LLM_MODEL")))
		log.Info("plain-language compiler enabled", "model", config.Get("WARRANT_LLM_MODEL", compiler.DefaultModel))
	} else {
		log.Warn("ANTHROPIC_API_KEY not set: plain-language compiler disabled (templates still work)")
	}

	// Console (Phase 5): serve the built web app if present.
	if dir := config.Get("WARRANT_CONSOLE_DIR", "console/dist"); dirExists(dir) {
		cfg.Console = spa(dir)
		log.Info("serving console", "dir", dir)
	}

	handler, err := api.New(l, cfg)
	if err != nil {
		return err
	}

	go sweep(ctx, log, l)

	addr := config.Get("WARRANT_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("warrant api listening", "addr", addr, "signing_key", sig.KeyID(), "reservation_ttl", ttl)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

// sweep releases expired holds every 30 seconds.
func sweep(ctx context.Context, log *slog.Logger, l *ledger.Ledger) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := l.ReleaseExpired(ctx)
			if err != nil {
				log.Error("sweeper", "err", err)
			} else if n > 0 {
				log.Info("released expired holds", "count", n)
			}
		}
	}
}

func dirExists(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !st.IsDir()
}

// spa serves static files from dir and falls back to index.html.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			http.Error(w, `{"error":{"code":"not_found","message":"not found"}}`, http.StatusNotFound)
			return
		}
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
