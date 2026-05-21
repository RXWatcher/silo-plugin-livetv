CREATE TABLE m3u_sources (
    id text PRIMARY KEY,
    name text NOT NULL,
    url text NOT NULL,
    http_headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    refresh_interval interval NOT NULL DEFAULT '6 hours',
    last_refreshed_at timestamptz,
    last_status text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    last_modified text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE xmltv_sources (
    id text PRIMARY KEY,
    name text NOT NULL,
    url text NOT NULL,
    http_headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    refresh_interval interval NOT NULL DEFAULT '3 hours',
    last_refreshed_at timestamptz,
    last_status text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    last_modified text NOT NULL DEFAULT '',
    gzip boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    default_m3u_refresh interval NOT NULL DEFAULT '6 hours',
    default_xmltv_refresh interval NOT NULL DEFAULT '3 hours',
    guide_window_cap interval NOT NULL DEFAULT '24 hours',
    per_user_stream_cap int NOT NULL DEFAULT 3,
    per_channel_default_cap int NOT NULL DEFAULT 5,
    session_idle_timeout interval NOT NULL DEFAULT '60 seconds',
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO settings (id) VALUES (1) ON CONFLICT DO NOTHING;
