CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY,
    client TEXT NOT NULL,
    erp TEXT NOT NULL,
    anzsco TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL DEFAULT '',
    member TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('processing', 'completed', 'duplicate')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS documents_erp_lower_idx ON documents (lower(erp));
CREATE INDEX IF NOT EXISTS documents_created_at_idx ON documents (created_at DESC);

CREATE TABLE IF NOT EXISTS sources (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    content_sha256 TEXT,
    uniqueness TEXT NOT NULL DEFAULT 'unique' CHECK (uniqueness IN ('unique', 'duplicate')),
    score DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sources_document_id_idx ON sources (document_id, created_at);
CREATE INDEX IF NOT EXISTS sources_sha256_idx ON sources (content_sha256) WHERE content_sha256 IS NOT NULL;

CREATE TABLE IF NOT EXISTS duplicate_matches (
    id UUID PRIMARY KEY,
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    matched_source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    erp TEXT NOT NULL,
    score DOUBLE PRECISION NOT NULL,
    uploaded_at TIMESTAMPTZ NOT NULL,
    UNIQUE (source_id, matched_source_id)
);

CREATE INDEX IF NOT EXISTS duplicate_matches_source_id_idx ON duplicate_matches (source_id);

CREATE TABLE IF NOT EXISTS notifications (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    detail TEXT NOT NULL,
    "read" BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_created_at_idx ON notifications (created_at DESC);
CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications (created_at DESC) WHERE "read" = FALSE;
