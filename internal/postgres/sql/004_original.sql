ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_status_check;
ALTER TABLE documents
    ADD CONSTRAINT documents_status_check
    CHECK (status IN ('processing', 'completed', 'original', 'duplicate'));

ALTER TABLE sources DROP CONSTRAINT IF EXISTS sources_uniqueness_check;
ALTER TABLE sources
    ADD CONSTRAINT sources_uniqueness_check
    CHECK (uniqueness IN ('unique', 'original', 'duplicate'));

UPDATE sources s
SET uniqueness = 'original'
WHERE s.uniqueness = 'duplicate'
  AND NOT EXISTS (
      SELECT 1
      FROM duplicate_matches dm
      JOIN sources earlier ON earlier.id = dm.matched_source_id
      WHERE dm.source_id = s.id
        AND (
            earlier.created_at < s.created_at
            OR (earlier.created_at = s.created_at AND earlier.id < s.id)
        )
  );

UPDATE documents d
SET status = CASE
    WHEN EXISTS (
        SELECT 1 FROM sources s
        WHERE s.document_id = d.id AND s.content_sha256 IS NULL
    ) THEN 'processing'
    WHEN EXISTS (
        SELECT 1 FROM sources s
        WHERE s.document_id = d.id AND s.uniqueness = 'duplicate'
    ) THEN 'duplicate'
    WHEN EXISTS (
        SELECT 1 FROM sources s
        WHERE s.document_id = d.id AND s.uniqueness = 'original'
    ) THEN 'original'
    ELSE 'completed'
END
WHERE d.status <> 'processing';
