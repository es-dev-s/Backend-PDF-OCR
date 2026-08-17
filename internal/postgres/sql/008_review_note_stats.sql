ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS review_note TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS review_requested_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS documents_review_requested_at_idx
    ON documents (review_requested_at DESC)
    WHERE status = 'pending_review';

CREATE INDEX IF NOT EXISTS sources_created_at_idx
    ON sources (created_at);
