package config

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListenAddr         = ":8080"
	DefaultMaxBodyBytes       = 5 << 20
	DefaultMaxLoggedTextBytes = 4 << 10
	DefaultUpstreamTimeoutSec = 300
)

// RouteRule maps a model name prefix to an upstream backend.
type RouteRule struct {
	Prefix  string `json:"prefix"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// Config holds all runtime configuration for the proxy.
// Values are loaded once at startup from environment variables.
type Config struct {
	ListenAddr string

	// Upstream routing
	Routes       []RouteRule
	DefaultRoute RouteRule

	// Observability: Loki
	LokiPushURL string
	LokiLabels  map[string]string

	// PII policy
	PIIBlock bool

	// Redis connection
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Token quota (per-user daily)
	DailyTokenLimit                 int
	UserDailyTokenLimitOverride     map[string]int
	TokenSafetyFactor               float64
	DefaultMaxCompletionTokensChat  int
	DefaultMaxOutputTokensResponses int
	QuotaTimezone                   string // e.g. "Asia/Seoul"

	// RPS rate limit (per-user)
	RateLimitRPS             int
	UserRateLimitOverrideRPS map[string]int

	// Audit
	AuditStreamKey        string // Redis Stream key
	AuditRecordTTLSeconds int
	AuditStoreRecord      bool
	AuditHMACKey          string // if set, HMAC-SHA256; else plain SHA-256

	// Proxy behaviour
	UpstreamTimeout time.Duration

	// Inject include_usage for OpenAI chat streaming
	InjectChatStreamUsage       bool
	InjectChatStreamUsageOpenAI bool // limit injection to api.openai.com only
}

// Load reads configuration from environment variables, applying defaults where
// no value is set.  Call once at process startup.
func Load() Config {
	cfg := Config{
		ListenAddr: DefaultListenAddr,

		LokiPushURL: os.Getenv("LOKI_PUSH_URL"),
		LokiLabels:  parseKVEnv(os.Getenv("LOKI_LABELS")),

		PIIBlock: envBool(false, "PII_BLOCK"),

		RedisAddr:     envStr("localhost:6379", "REDIS_ADDR"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       envInt(0, "REDIS_DB"),

		DailyTokenLimit:                 envInt(200_000, "TOKEN_DAILY_LIMIT"),
		TokenSafetyFactor:               envFloat(1.20, "TOKEN_SAFETY_FACTOR"),
		DefaultMaxCompletionTokensChat:  envInt(1024, "DEFAULT_MAX_COMPLETION_TOKENS_CHAT"),
		DefaultMaxOutputTokensResponses: envInt(1024, "DEFAULT_MAX_OUTPUT_TOKENS_RESPONSES"),
		QuotaTimezone:                   envStr("Asia/Seoul", "QUOTA_TIMEZONE"),

		RateLimitRPS: envInt(5, "RATE_LIMIT_RPS"),

		AuditStreamKey:        envStr("audit:llm-proxy", "AUDIT_STREAM_KEY"),
		AuditRecordTTLSeconds: envInt(7*24*3600, "AUDIT_RECORD_TTL_SECONDS"),
		AuditStoreRecord:      envBool(true, "AUDIT_STORE_REQUEST_RECORD"),
		AuditHMACKey:          os.Getenv("AUDIT_HMAC_KEY"),

		UpstreamTimeout: time.Duration(envInt(DefaultUpstreamTimeoutSec, "UPSTREAM_TIMEOUT_SECONDS")) * time.Second,

		InjectChatStreamUsage:       envBool(false, "INJECT_CHAT_STREAM_USAGE"),
		InjectChatStreamUsageOpenAI: envBool(true, "INJECT_CHAT_STREAM_USAGE_OPENAI_ONLY"),

		UserDailyTokenLimitOverride: map[string]int{},
		UserRateLimitOverrideRPS:    map[string]int{},
	}

	if s := os.Getenv("USER_TOKEN_LIMITS_JSON"); s != "" {
		if err := json.Unmarshal([]byte(s), &cfg.UserDailyTokenLimitOverride); err != nil {
			log.Printf("warn: USER_TOKEN_LIMITS_JSON parse error: %v", err)
		}
	}
	if s := os.Getenv("USER_RATE_LIMITS_JSON"); s != "" {
		if err := json.Unmarshal([]byte(s), &cfg.UserRateLimitOverrideRPS); err != nil {
			log.Printf("warn: USER_RATE_LIMITS_JSON parse error: %v", err)
		}
	}

	if s := os.Getenv("ROUTES_JSON"); s != "" {
		if err := json.Unmarshal([]byte(s), &cfg.Routes); err != nil {
			log.Fatalf("ROUTES_JSON parse error: %v", err)
		}
	}
	if len(cfg.Routes) == 0 {
		base := envStr("https://api.openai.com", "UPSTREAM_BASE_URL")
		cfg.Routes = []RouteRule{{
			Prefix:  "",
			Name:    "default",
			BaseURL: base,
			APIKey:  os.Getenv("UPSTREAM_API_KEY"),
		}}
	}
	cfg.DefaultRoute = cfg.Routes[0]
	return cfg
}

// ---------- env helpers ----------

func envStr(def, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(def int, key string) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envFloat(def float64, key string) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(def bool, key string) bool {
	if v := os.Getenv(key); v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "1" || v == "true" || v == "yes" || v == "y"
	}
	return def
}

func parseKVEnv(s string) map[string]string {
	out := map[string]string{}
	for _, item := range strings.Split(strings.TrimSpace(s), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out
}
