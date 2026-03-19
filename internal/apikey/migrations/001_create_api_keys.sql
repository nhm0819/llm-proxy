CREATE TABLE IF NOT EXISTS api_keys (
    key         TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys (user_id);
