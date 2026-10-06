-- +goose Up
-- +goose StatementBegin
INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin)
SELECT l.id, q.elem, q.elem, (q.ord - 1), TRUE, 'map', 'manual', 'service'
FROM project_locations l
CROSS JOIN LATERAL jsonb_array_elements_text(l.queries) WITH ORDINALITY AS q(elem, ord)
WHERE length(btrim(q.elem)) > 0
ON CONFLICT (location_id, kind, normalized) DO NOTHING;
ALTER TABLE project_locations DROP COLUMN queries;
ALTER TABLE project_locations DROP COLUMN query_service;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Best-effort legacy restore: re-add the columns and copy the enabled map
-- draft rows back as a JSON array. No history is reconstructed and the
-- pre-switch query_service values are not recoverable, so they stay empty.
ALTER TABLE project_locations ADD COLUMN queries JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(queries) = 'array');
ALTER TABLE project_locations ADD COLUMN query_service TEXT NOT NULL DEFAULT '';
UPDATE project_locations l SET queries = COALESCE(
    (SELECT jsonb_agg(q.text ORDER BY q.ordinal, q.id)
     FROM project_location_queries q
     WHERE q.location_id = l.id AND q.kind = 'map' AND q.enabled),
    '[]'::jsonb);
-- +goose StatementEnd
