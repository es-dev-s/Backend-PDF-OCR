ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS title_norm TEXT NOT NULL DEFAULT '';
ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS title_similar_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS title_similarities (
    id UUID PRIMARY KEY,
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    matched_source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    score DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_id, matched_source_id),
    CHECK (source_id <> matched_source_id),
    CHECK (score >= 0 AND score <= 1)
);

CREATE INDEX IF NOT EXISTS title_similarities_source_idx
    ON title_similarities (source_id, score DESC);

CREATE INDEX IF NOT EXISTS sources_title_norm_idx
    ON sources (title_norm)
    WHERE title_norm <> '';

CREATE INDEX IF NOT EXISTS sources_title_similar_pending_idx
    ON sources (created_at)
    WHERE title_similar_at IS NULL AND needs_title = FALSE;
