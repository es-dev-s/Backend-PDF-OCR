ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';

UPDATE sources s
SET note = d.review_note
FROM documents d
WHERE d.id = s.document_id
  AND s.note = ''
  AND COALESCE(d.review_note, '') <> '';

CREATE INDEX IF NOT EXISTS documents_owner_created_at_idx
    ON documents (owner_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS documents_pending_created_at_idx
    ON documents (created_at DESC, id DESC)
    WHERE status = 'pending_review';
