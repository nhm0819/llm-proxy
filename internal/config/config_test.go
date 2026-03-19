package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/nhm0819/llm-proxy/internal/config"
)

// setEnvs sets multiple env vars and returns a cleanup func.
func setEnvs(t *testing.T, kvs map[string]string) {
	t.Helper()
	for k, v := range kvs {
		t.Setenv(k, v)
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might override defaults
	for _, key := range []string{
		"UPSTREAM_BASE_URL", "UPSTREAM_API_KEY", "ROUTES_JSON",
		"REDIS_ADDR", "TOKEN_DAILY_LIMIT", "TOKEN_SAFETY_FACTOR",
		"RATE_LIMIT_RPS", "PII_BLOCK", "QUOTA_TIMEZONE",
		"LOKI_PUSH_URL", "DATABASE_URL", "ADMIN_API_KEY",
	} {
		t.Setenv(key, "")
	}

	// Need to explicitly unset to get defaults
	os.Unsetenv("REDIS_ADDR")
	os.Unsetenv("TOKEN_DAILY_LIMIT")
	os.Unsetenv("TOKEN_SAFETY_FACTOR")
	os.Unsetenv("RATE_LIMIT_RPS")
	os.Unsetenv("QUOTA_TIMEZONE")

	cfg := config.Load()

	if cfg.ListenAddr != ":8080" {
		t.Errorf("expected ListenAddr=:8080, got %q", cfg.ListenAddr)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("expected RedisAddr=localhost:6379, got %q", cfg.RedisAddr)
	}
	if cfg.DailyTokenLimit != 200_000 {
		t.Errorf("expected DailyTokenLimit=200000, got %d", cfg.DailyTokenLimit)
	}
	if cfg.TokenSafetyFactor != 1.20 {
		t.Errorf("expected TokenSafetyFactor=1.20, got %f", cfg.TokenSafetyFactor)
	}
	if cfg.RateLimitRPS != 5 {
		t.Errorf("expected RateLimitRPS=5, got %d", cfg.RateLimitRPS)
	}
	if cfg.QuotaTimezone != "Asia/Seoul" {
		t.Errorf("expected QuotaTimezone=Asia/Seoul, got %q", cfg.QuotaTimezone)
	}
	if cfg.PIIBlock {
		t.Error("expected PIIBlock=false by default")
	}
	if len(cfg.Routes) == 0 {
		t.Fatal("expected at least one default route")
	}
	if cfg.DefaultRoute.Name != "default" {
		t.Errorf("expected DefaultRoute.Name=default, got %q", cfg.DefaultRoute.Name)
	}
}

func TestLoad_OverridesFromEnv(t *testing.T) {
	setEnvs(t, map[string]string{
		"REDIS_ADDR":         "redis.example.com:6380",
		"TOKEN_DAILY_LIMIT":  "500000",
		"TOKEN_SAFETY_FACTOR": "1.50",
		"RATE_LIMIT_RPS":     "10",
		"PII_BLOCK":          "true",
		"QUOTA_TIMEZONE":     "UTC",
		"LOKI_PUSH_URL":      "http://loki:3100/loki/api/v1/push",
		"DATABASE_URL":       "postgres://user:pass@localhost:5432/db",
		"ADMIN_API_KEY":      "secret-admin",
	})

	cfg := config.Load()

	if cfg.RedisAddr != "redis.example.com:6380" {
		t.Errorf("expected REDIS_ADDR override, got %q", cfg.RedisAddr)
	}
	if cfg.DailyTokenLimit != 500_000 {
		t.Errorf("expected DailyTokenLimit=500000, got %d", cfg.DailyTokenLimit)
	}
	if cfg.TokenSafetyFactor != 1.50 {
		t.Errorf("expected TokenSafetyFactor=1.50, got %f", cfg.TokenSafetyFactor)
	}
	if cfg.RateLimitRPS != 10 {
		t.Errorf("expected RateLimitRPS=10, got %d", cfg.RateLimitRPS)
	}
	if !cfg.PIIBlock {
		t.Error("expected PIIBlock=true")
	}
	if cfg.QuotaTimezone != "UTC" {
		t.Errorf("expected QuotaTimezone=UTC, got %q", cfg.QuotaTimezone)
	}
	if cfg.LokiPushURL != "http://loki:3100/loki/api/v1/push" {
		t.Errorf("unexpected LokiPushURL: %q", cfg.LokiPushURL)
	}
	if cfg.DatabaseURL != "postgres://user:pass@localhost:5432/db" {
		t.Errorf("unexpected DatabaseURL: %q", cfg.DatabaseURL)
	}
	if cfg.AdminAPIKey != "secret-admin" {
		t.Errorf("expected AdminAPIKey=secret-admin, got %q", cfg.AdminAPIKey)
	}
}

func TestLoad_RoutesJSON(t *testing.T) {
	setEnvs(t, map[string]string{
		"ROUTES_JSON": `[{"prefix":"gpt","name":"openai","base_url":"https://api.openai.com","api_key":"sk-xxx"},{"prefix":"claude","name":"anthropic","base_url":"https://api.anthropic.com","api_key":"sk-yyy"}]`,
	})

	cfg := config.Load()

	if len(cfg.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(cfg.Routes))
	}
	if cfg.Routes[0].Name != "openai" {
		t.Errorf("expected first route name=openai, got %q", cfg.Routes[0].Name)
	}
	if cfg.Routes[1].Prefix != "claude" {
		t.Errorf("expected second route prefix=claude, got %q", cfg.Routes[1].Prefix)
	}
	if cfg.DefaultRoute.Name != "openai" {
		t.Errorf("DefaultRoute should be first route, got %q", cfg.DefaultRoute.Name)
	}
}

func TestLoad_UpstreamTimeout(t *testing.T) {
	setEnvs(t, map[string]string{
		"UPSTREAM_TIMEOUT_SECONDS": "60",
	})

	cfg := config.Load()

	if cfg.UpstreamTimeout != 60*time.Second {
		t.Errorf("expected UpstreamTimeout=60s, got %v", cfg.UpstreamTimeout)
	}
}

func TestLoad_UserLimitsJSON(t *testing.T) {
	setEnvs(t, map[string]string{
		"USER_TOKEN_LIMITS_JSON": `{"alice":1000000,"bob":500000}`,
		"USER_RATE_LIMITS_JSON":  `{"alice":20}`,
	})

	cfg := config.Load()

	if cfg.UserDailyTokenLimitOverride["alice"] != 1000000 {
		t.Errorf("expected alice limit=1000000, got %d", cfg.UserDailyTokenLimitOverride["alice"])
	}
	if cfg.UserRateLimitOverrideRPS["alice"] != 20 {
		t.Errorf("expected alice RPS=20, got %d", cfg.UserRateLimitOverrideRPS["alice"])
	}
}

func TestLoad_LokiLabels(t *testing.T) {
	setEnvs(t, map[string]string{
		"LOKI_LABELS":   "app=llm-proxy,env=test",
		"LOKI_PUSH_URL": "http://loki:3100",
	})

	cfg := config.Load()

	if cfg.LokiLabels["app"] != "llm-proxy" {
		t.Errorf("expected app=llm-proxy, got %q", cfg.LokiLabels["app"])
	}
	if cfg.LokiLabels["env"] != "test" {
		t.Errorf("expected env=test, got %q", cfg.LokiLabels["env"])
	}
}

func TestLoad_BoolVariants(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "y", "TRUE", "Yes"} {
		t.Run("PII_BLOCK="+v, func(t *testing.T) {
			t.Setenv("PII_BLOCK", v)
			cfg := config.Load()
			if !cfg.PIIBlock {
				t.Errorf("expected PIIBlock=true for value %q", v)
			}
		})
	}
	for _, v := range []string{"0", "false", "no", "n", ""} {
		t.Run("PII_BLOCK="+v, func(t *testing.T) {
			t.Setenv("PII_BLOCK", v)
			cfg := config.Load()
			if cfg.PIIBlock {
				t.Errorf("expected PIIBlock=false for value %q", v)
			}
		})
	}
}
