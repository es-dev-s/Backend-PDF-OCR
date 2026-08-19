-- Similar titles are stored only at >= 70% Dice. Recompute so weaker
-- pairs drop out of the Similar column.
DELETE FROM title_similarities;
UPDATE sources SET title_similar_at = NULL;
