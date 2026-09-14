-- +goose Up
-- +goose StatementBegin

-- Backfill: profiles that predate location questions get a deterministic maps
-- query built from their category + location, so the "Test your rankings"
-- button works for every existing project immediately. The next profile save
-- regenerates it through the LLM pass and replaces this value.
UPDATE project_ai_questions q
SET location_questions = jsonb_build_array(
        'best '
        || COALESCE(NULLIF(btrim(bp.primary_category), ''), 'businesses')
        || ' near '
        || btrim(bp.primary_location)
    )
FROM project_business_profile bp
WHERE bp.project_id = q.project_id
  AND bp.primary_location IS NOT NULL
  AND btrim(bp.primary_location) <> ''
  AND (q.location_questions IS NULL OR q.location_questions = '[]'::jsonb);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Nothing to undo: the column keeps whatever value it has after the backfill.
SELECT 1;

-- +goose StatementEnd
