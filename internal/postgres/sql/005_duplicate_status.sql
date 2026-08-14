UPDATE documents d
SET status = CASE
    WHEN EXISTS (
        SELECT 1 FROM sources s
        WHERE s.document_id = d.id AND s.content_sha256 IS NULL
    ) THEN 'processing'
    WHEN EXISTS (
        SELECT 1 FROM sources s
        WHERE s.document_id = d.id
          AND s.uniqueness IN ('duplicate', 'original')
    ) THEN 'duplicate'
    ELSE 'completed'
END
WHERE d.status IN ('original', 'duplicate', 'completed');
