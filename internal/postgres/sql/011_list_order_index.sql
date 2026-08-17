-- 001 already owns documents_created_at_idx on (created_at DESC), so the
-- keyset ordering used by the capped listing needs its own name to exist at all.
CREATE INDEX IF NOT EXISTS documents_created_at_id_idx
    ON documents (created_at DESC, id DESC);
