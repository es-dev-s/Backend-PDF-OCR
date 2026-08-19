-- Dice title similarity (and same-document sibling compares) replaces the
-- old 90%-of-longer-title gate, which only kept near-identical duplicates.
DELETE FROM title_similarities;
UPDATE sources SET title_similar_at = NULL;
