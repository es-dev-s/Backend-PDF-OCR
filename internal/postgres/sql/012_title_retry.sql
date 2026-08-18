ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS title_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS next_title_at TIMESTAMPTZ;

-- Catch rows that stored a placeholder title and left the queue.
UPDATE sources
SET needs_title = TRUE
WHERE needs_title = FALSE
  AND (
    title = ''
    OR lower(title) LIKE '%.pdf'
    OR title = 'Untitled document'
  )
  AND title IS DISTINCT FROM 'Title not readable (scanned PDF)';

CREATE INDEX IF NOT EXISTS sources_title_retry_idx
    ON sources (next_title_at, created_at)
    WHERE needs_title = TRUE;
