ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS notified_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS sources_unhashed_idx
    ON sources (created_at)
    WHERE content_sha256 IS NULL;
