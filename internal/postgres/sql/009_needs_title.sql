ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS needs_title BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE sources
SET needs_title = TRUE
WHERE needs_title = FALSE
  AND (
    title = ''
    OR lower(title) LIKE '%.pdf'
    OR title = 'Untitled document'
  )
  AND title IS DISTINCT FROM 'Title not readable (scanned PDF)';

CREATE INDEX IF NOT EXISTS sources_needs_title_idx
    ON sources (created_at)
    WHERE needs_title = TRUE;
