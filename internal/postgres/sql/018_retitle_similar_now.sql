-- Local matcher must recompute before a hosted API with the old 90%
-- gate seals rows at 0 hits. Unique files with overlapping titles had
-- title_similar_at set and never ran again.
DELETE FROM title_similarities;
UPDATE sources SET title_similar_at = NULL;
