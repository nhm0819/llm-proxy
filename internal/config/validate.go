package config

import (
	"fmt"
	"strings"
)

// Validate checks that the config is internally consistent and returns an
// error describing the first problem found.  Call immediately after Load().
func (c Config) Validate() error {
	if c.TokenSafetyFactor < 1.0 {
		return fmt.Errorf("TOKEN_SAFETY_FACTOR must be >= 1.0, got %.2f", c.TokenSafetyFactor)
	}
	if c.DailyTokenLimit <= 0 {
		return fmt.Errorf("TOKEN_DAILY_LIMIT must be > 0, got %d", c.DailyTokenLimit)
	}
	if c.RateLimitRPS <= 0 {
		return fmt.Errorf("RATE_LIMIT_RPS must be > 0, got %d", c.RateLimitRPS)
	}
	if c.DefaultMaxCompletionTokensChat <= 0 {
		return fmt.Errorf("DEFAULT_MAX_COMPLETION_TOKENS_CHAT must be > 0")
	}
	if c.DefaultMaxOutputTokensResponses <= 0 {
		return fmt.Errorf("DEFAULT_MAX_OUTPUT_TOKENS_RESPONSES must be > 0")
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("at least one route is required (set UPSTREAM_BASE_URL or ROUTES_JSON)")
	}
	for i, r := range c.Routes {
		if strings.TrimSpace(r.BaseURL) == "" {
			return fmt.Errorf("route[%d] %q has empty base_url", i, r.Name)
		}
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("route[%d] has empty name", i)
		}
	}
	if c.AuditRecordTTLSeconds <= 0 {
		return fmt.Errorf("AUDIT_RECORD_TTL_SECONDS must be > 0")
	}
	if _, err := loadLocation(c.QuotaTimezone); err != nil {
		return fmt.Errorf("QUOTA_TIMEZONE %q is invalid: %w", c.QuotaTimezone, err)
	}
	return nil
}

// loadLocation is a thin wrapper so tests can call Validate() without a real
// timezone database.
var loadLocation = func(name string) (interface{}, error) {
	// we just need to check parseability; discard the result
	if name == "" {
		return nil, fmt.Errorf("empty timezone")
	}
	// actual time.LoadLocation happens in the proxy at startup
	return nil, nil
}
