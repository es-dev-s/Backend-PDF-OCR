-- Word-level title similarity replaces full-string Levenshtein, which
-- could group unrelated files from the same document under one heading.
DELETE FROM title_similarities;
UPDATE sources SET title_similar_at = NULL;
