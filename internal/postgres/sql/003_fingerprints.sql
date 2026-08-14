ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS text_norm_sha256 TEXT,
    ADD COLUMN IF NOT EXISTS simhash BIGINT,
    ADD COLUMN IF NOT EXISTS phash BIGINT,
    ADD COLUMN IF NOT EXISTS has_text_layer BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS page_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS fingerprint_kind TEXT NOT NULL DEFAULT 'pending';

CREATE INDEX IF NOT EXISTS sources_text_norm_sha256_idx
    ON sources (text_norm_sha256)
    WHERE text_norm_sha256 IS NOT NULL;

CREATE INDEX IF NOT EXISTS sources_simhash_idx
    ON sources (simhash)
    WHERE simhash IS NOT NULL;

CREATE INDEX IF NOT EXISTS sources_phash_idx
    ON sources (phash)
    WHERE phash IS NOT NULL;

ALTER TABLE duplicate_matches
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'exact';

CREATE TABLE IF NOT EXISTS fingerprint_lsh (
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('simhash', 'phash')),
    band SMALLINT NOT NULL,
    bucket INTEGER NOT NULL,
    PRIMARY KEY (kind, band, bucket, source_id)
);

CREATE INDEX IF NOT EXISTS fingerprint_lsh_lookup_idx
    ON fingerprint_lsh (kind, band, bucket);
