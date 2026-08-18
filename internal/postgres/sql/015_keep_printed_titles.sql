-- Printed titles must never stay in the extraction queue. A later untitled
-- engine result used to overwrite them on retry.
UPDATE sources
SET needs_title = FALSE,
    next_title_at = NULL
WHERE needs_title = TRUE
  AND title <> ''
  AND title <> 'Untitled document'
  AND lower(title) NOT LIKE '%.pdf';
