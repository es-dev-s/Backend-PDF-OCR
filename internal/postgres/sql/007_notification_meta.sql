ALTER TABLE notifications ADD COLUMN IF NOT EXISTS document_id UUID REFERENCES documents(id) ON DELETE SET NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS notifications_document_id_idx ON notifications (document_id);
CREATE INDEX IF NOT EXISTS notifications_kind_idx ON notifications (kind);
