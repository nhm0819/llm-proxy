package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/audit"
	"github.com/nhm0819/llm-proxy/internal/breaker"
	"github.com/nhm0819/llm-proxy/internal/config"
	"github.com/nhm0819/llm-proxy/internal/loki"
	"github.com/nhm0819/llm-proxy/internal/metrics"
	"github.com/nhm0819/llm-proxy/internal/middleware"
	"github.com/nhm0819/llm-proxy/internal/pii"
	"github.com/nhm0819/llm-proxy/internal/proxy"
	"github.com/nhm0819/llm-proxy/internal/quota"
	"github.com/nhm0819/llm-proxy/internal/ratelimit"
	"github.com/nhm0819/llm-proxy/internal/router"
	"github.com/nhm0819/llm-proxy/internal/tokencount"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	rdb := mustRedis(cfg)

	m := metrics.New()

	cbTransport := breaker.New(http.DefaultTransport, breaker.BreakerConfig{
		MaxFailures: 5,
		OpenTimeout: 30 * time.Second,
	})
	httpClient := &http.Client{
		Timeout:   cfg.UpstreamTimeout,
		Transport: cbTransport,
	}

	apiKeyReg := middleware.LoadAPIKeyRegistry()

	deps := proxy.Dependencies{
		Router:     router.New(cfg),
		PIIScanner: pii.New(),
		TokenCount: tokencount.New(),
		QuotaStore: quota.New(rdb),
		RLStore:    ratelimit.New(rdb),
		AuditStore: audit.New(rdb, audit.Config{
			StreamKey:    cfg.AuditStreamKey,
			RecordTTLSec: cfg.AuditRecordTTLSeconds,
			StoreRecord:  cfg.AuditStoreRecord,
			HMACKey:      cfg.AuditHMACKey,
		}),
		LokiPusher: loki.New(cfg.LokiPushURL, cfg.LokiLabels),
		Metrics:    m,
		HTTPClient: httpClient,
	}

	proxyHandler := proxy.New(cfg, deps)

	mux := http.NewServeMux()
	mux.Handle("/v1/", middleware.Chain(
		proxyHandler,
		middleware.AuthAPIKey(apiKeyReg),
		middleware.Recover,
		middleware.RequestID,
		middleware.AccessLog,
		middleware.MaxBody(config.DefaultMaxBodyBytes),
	))
	mux.HandleFunc("/healthz", healthz)
	mux.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("llm-proxy listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("signal %s: shutting down...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("bye")
}

func mustRedis(cfg config.Config) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed (addr=%s): %v", cfg.RedisAddr, err)
	}
	log.Printf("redis connected: %s db=%d", cfg.RedisAddr, cfg.RedisDB)
	return rdb
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
