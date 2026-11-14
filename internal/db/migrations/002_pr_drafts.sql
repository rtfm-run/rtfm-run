-- A verified fix, prepared as a pull request by the worker: the proposal,
-- the repository's contribution guidelines and the agent's reading of them.
-- Opening it later needs only this row, not the worker's disk.
CREATE TABLE pr_drafts (
    run_id      BIGINT PRIMARY KEY REFERENCES runs ON DELETE CASCADE,
    proposal    JSONB NOT NULL,              -- prs.Proposal before guidelines are applied
    guidelines  JSONB,                       -- guidelines.Found (NULL: not a GitHub repo or unreadable)
    decision    JSONB,                       -- guidelines.Decision (NULL: no guidelines found)
    partial     BOOLEAN NOT NULL DEFAULT false, -- the fix improved but didn't fully fix the doc
    status      TEXT NOT NULL DEFAULT 'ready',  -- ready | opening | opened
    error       TEXT NOT NULL DEFAULT '',    -- the last failed attempt to open it
    opening_at  TIMESTAMPTZ,                 -- when the current attempt started (stale after 10 minutes)
    pr_url      TEXT NOT NULL DEFAULT '',
    opened_by   BIGINT REFERENCES users ON DELETE SET NULL,
    opened_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
