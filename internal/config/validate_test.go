package config_test

import (
	"testing"

	"github.com/nhm0819/llm-proxy/internal/config"
)

func validConfig() config.Config {
	return config.Config{
		ListenAddr:                      ":8080",
		Routes:                          []config.RouteRule{{Name: "default", BaseURL: "https://api.openai.com"}},
		DefaultRoute:                    config.RouteRule{Name: "default", BaseURL: "https://api.openai.com"},
		DailyTokenLimit:                 200_000,
		TokenSafetyFactor:               1.2,
		DefaultMaxCompletionTokensChat:  1024,
		DefaultMaxOutputTokensResponses: 1024,
		QuotaTimezone:                   "Asia/Seoul",
		RateLimitRPS:                    5,
		AuditStreamKey:                  "audit:llm-proxy",
		AuditRecordTTLSeconds:           604800,
		RedisAddr:                       "localhost:6379",
		MaxBodyBytes:                    20 << 20,
		UserDailyTokenLimitOverride:     map[string]int{},
		UserRateLimitOverrideRPS:        map[string]int{},
	}
}

func TestValidate_OK(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_SafetyFactor(t *testing.T) {
	c := validConfig()
	c.TokenSafetyFactor = 0.5
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for safety factor < 1.0")
	}
}

func TestValidate_NoRoutes(t *testing.T) {
	c := validConfig()
	c.Routes = nil
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty routes")
	}
}

func TestValidate_EmptyBaseURL(t *testing.T) {
	c := validConfig()
	c.Routes = []config.RouteRule{{Name: "bad", BaseURL: ""}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty base_url")
	}
}

func TestValidate_ZeroDailyLimit(t *testing.T) {
	c := validConfig()
	c.DailyTokenLimit = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for zero daily token limit")
	}
}

func TestValidate_ZeroRPS(t *testing.T) {
	c := validConfig()
	c.RateLimitRPS = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for zero RPS limit")
	}
}

func TestValidate_MaxBodyBytes_Zero(t *testing.T) {
	c := validConfig()
	c.MaxBodyBytes = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for zero MaxBodyBytes")
	}
}

func TestValidate_MaxBodyBytes_Negative(t *testing.T) {
	c := validConfig()
	c.MaxBodyBytes = -1
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for negative MaxBodyBytes")
	}
}

func TestValidate_MaxBodyBytes_TooLarge(t *testing.T) {
	c := validConfig()
	c.MaxBodyBytes = 101 << 20 // 101MB
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for MaxBodyBytes > 100MB")
	}
}

func TestValidate_MaxBodyBytes_AtLimit(t *testing.T) {
	c := validConfig()
	c.MaxBodyBytes = 100 << 20 // exactly 100MB
	if err := c.Validate(); err != nil {
		t.Fatalf("expected no error for MaxBodyBytes at 100MB, got: %v", err)
	}
}
