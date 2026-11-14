-- vanguard.run: initial schema.

CREATE TABLE users (
    id               BIGSERIAL PRIMARY KEY,
    github_id        BIGINT NOT NULL UNIQUE,
    login            TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    email            TEXT NOT NULL DEFAULT '',
    avatar_url       TEXT NOT NULL DEFAULT '',
    -- GitHub App user-to-server tokens, AES-GCM encrypted.
    access_token_enc  BYTEA,
    refresh_token_enc BYTEA,
    token_expires_at  TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,           -- sha256 of the cookie value
    user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE oauth_states (
    state      TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE installations (
    id            BIGINT PRIMARY KEY,       -- GitHub installation id
    account_id    BIGINT NOT NULL,
    account_login TEXT NOT NULL,
    account_type  TEXT NOT NULL,            -- User | Organization
    suspended     BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which installations a user can see (from GET /user/installations).
CREATE TABLE user_installations (
    user_id         BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    installation_id BIGINT NOT NULL REFERENCES installations ON DELETE CASCADE,
    PRIMARY KEY (user_id, installation_id)
);

CREATE TABLE repos (
    id              BIGINT PRIMARY KEY,     -- GitHub repository id
    installation_id BIGINT NOT NULL REFERENCES installations ON DELETE CASCADE,
    full_name       TEXT NOT NULL,
    default_branch  TEXT NOT NULL DEFAULT '',
    private         BOOLEAN NOT NULL DEFAULT false,
    html_url        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX repos_installation ON repos (installation_id);
CREATE INDEX repos_full_name ON repos (lower(full_name));

-- A quickstart is one doc in one repo to test.
CREATE TABLE quickstarts (
    id            BIGSERIAL PRIMARY KEY,
    repo_id       BIGINT NOT NULL REFERENCES repos ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    doc_path      TEXT NOT NULL,
    goal          TEXT NOT NULL,
    success_check TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT '',  -- '' = the repo's default branch
    backend       TEXT NOT NULL DEFAULT 'auto',
    model         TEXT NOT NULL DEFAULT '',
    fix           BOOLEAN NOT NULL DEFAULT true,
    verify        BOOLEAN NOT NULL DEFAULT true,
    created_by    BIGINT REFERENCES users ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, doc_path, branch)
);

-- When to run a quickstart automatically.
CREATE TABLE triggers (
    id            BIGSERIAL PRIMARY KEY,
    quickstart_id BIGINT NOT NULL REFERENCES quickstarts ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('push')),
    branches      TEXT[] NOT NULL DEFAULT '{}',  -- globs; empty = the quickstart's branch
    paths         TEXT[] NOT NULL DEFAULT '{}',  -- globs; empty = any change
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX triggers_quickstart ON triggers (quickstart_id);

CREATE TABLE runs (
    id            BIGSERIAL PRIMARY KEY,
    quickstart_id BIGINT NOT NULL REFERENCES quickstarts ON DELETE CASCADE,
    repo_id       BIGINT NOT NULL REFERENCES repos ON DELETE CASCADE,
    trigger       TEXT NOT NULL CHECK (trigger IN ('manual', 'push')),
    triggered_by  BIGINT REFERENCES users ON DELETE SET NULL,
    ref           TEXT NOT NULL DEFAULT '',     -- branch
    commit_sha    TEXT NOT NULL DEFAULT '',     -- requested (push) or tested commit
    status        TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    verdict       TEXT NOT NULL DEFAULT '',     -- works | broken | blocked | error
    issues        INT NOT NULL DEFAULT 0,
    doc_issues    INT NOT NULL DEFAULT 0,
    fix_status    TEXT NOT NULL DEFAULT '',
    pr_url        TEXT NOT NULL DEFAULT '',
    report_id     TEXT NOT NULL DEFAULT '',     -- report.Run.ID
    error         TEXT NOT NULL DEFAULT '',
    tokens        INT NOT NULL DEFAULT 0,
    worker        TEXT NOT NULL DEFAULT '',
    attempt       INT NOT NULL DEFAULT 0,
    cancel_requested BOOLEAN NOT NULL DEFAULT false,
    queued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    report        JSONB,                        -- full report.Run (fix + verify inside)
    verify_report JSONB
);
CREATE INDEX runs_queue ON runs (queued_at) WHERE status = 'queued';
CREATE INDEX runs_quickstart ON runs (quickstart_id, queued_at DESC);
CREATE INDEX runs_repo ON runs (repo_id, queued_at DESC);

-- One row per quickstart step per stage (newhire = original doc, verify =
-- the fixed doc).
CREATE TABLE run_steps (
    run_id       BIGINT NOT NULL REFERENCES runs ON DELETE CASCADE,
    stage        TEXT NOT NULL CHECK (stage IN ('newhire', 'verify')),
    idx          INT NOT NULL,
    step_key     TEXT NOT NULL,       -- stable across runs: doc location + normalized command
    doc_ref      TEXT NOT NULL DEFAULT '',
    instruction  TEXT NOT NULL DEFAULT '',
    doc_command  TEXT NOT NULL DEFAULT '',
    command      TEXT NOT NULL DEFAULT '',
    exit_code    INT NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,       -- pass | fail | skipped | unsupported
    failure      TEXT NOT NULL DEFAULT '',
    diagnosis    TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    output       TEXT NOT NULL DEFAULT '',
    observed_exit INT,                -- last real exit code of the command in the devbox log
    checks_out   BOOLEAN NOT NULL,    -- passed by following the doc, confirmed by the devbox log
    PRIMARY KEY (run_id, stage, idx)
);
CREATE INDEX run_steps_key ON run_steps (step_key);

CREATE TABLE run_events (
    id     BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES runs ON DELETE CASCADE,
    at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind   TEXT NOT NULL,
    data   JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX run_events_run ON run_events (run_id, id);

-- Test credentials a quickstart may request (encrypted).
CREATE TABLE credentials (
    id            BIGSERIAL PRIMARY KEY,
    quickstart_id BIGINT NOT NULL REFERENCES quickstarts ON DELETE CASCADE,
    name          TEXT NOT NULL,
    value_enc     BYTEA NOT NULL,
    created_by    BIGINT REFERENCES users ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (quickstart_id, name)
);

CREATE TABLE webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    event       TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    error       TEXT NOT NULL DEFAULT ''
);
