-- Matcher now waits for sibling titles and rematches after a printed
-- heading settles. Recompute so existing unique files with overlapping
-- titles (e.g. peanut / peanut system) appear under Similar.
DELETE FROM title_similarities;
UPDATE sources SET title_similar_at = NULL;
