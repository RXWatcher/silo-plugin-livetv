CREATE TABLE user_favorites (
    user_id text NOT NULL,
    channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    position int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX user_favorites_user_idx ON user_favorites (user_id, position);

CREATE TABLE user_recent (
    user_id text NOT NULL,
    channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    last_tuned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX user_recent_user_idx ON user_recent (user_id, last_tuned_at DESC);

CREATE TABLE stream_sessions (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    scoped_grant_id text NOT NULL,
    session_secret bytea NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    last_byte_at timestamptz NOT NULL DEFAULT now(),
    bytes_streamed bigint NOT NULL DEFAULT 0,
    client_ip inet,
    user_agent text NOT NULL DEFAULT '',
    ended_at timestamptz,
    end_reason text NOT NULL DEFAULT ''
);

CREATE INDEX stream_sessions_active_idx ON stream_sessions (channel_id) WHERE ended_at IS NULL;
CREATE INDEX stream_sessions_idle_idx   ON stream_sessions (last_byte_at) WHERE ended_at IS NULL;
