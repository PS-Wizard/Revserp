-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS project_keywords (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL,
    normalized_keyword TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('brand', 'non_brand')),
    source TEXT NOT NULL CHECK (source IN ('user', 'revserp')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, source, normalized_keyword)
);

WITH legacy_terms AS (
    SELECT p.project_id, elem.elem AS term, 'non_brand'::text AS kind, 0 AS precedence
    FROM project_business_profile AS p
    CROSS JOIN LATERAL jsonb_array_elements_text(p.non_branded_keywords) AS elem(elem)
    UNION ALL
    SELECT p.project_id, elem.elem, 'brand', 1
    FROM project_business_profile AS p
    CROSS JOIN LATERAL jsonb_array_elements_text(p.branded_keywords) AS elem(elem)
    UNION ALL
    SELECT p.project_id, elem.elem, 'non_brand', 2
    FROM project_business_profile AS p
    CROSS JOIN LATERAL jsonb_array_elements_text(p.target_keywords) AS elem(elem)
),
normalized_terms AS (
    SELECT DISTINCT ON (t.project_id, n.norm) t.project_id, d.display, n.norm, t.kind
    FROM legacy_terms AS t
    CROSS JOIN LATERAL (SELECT btrim(regexp_replace(t.term,
        -- normalize-class-start
        '[' || chr(9) || '-' || chr(13) || chr(32) || chr(133) || chr(160) || chr(5760) || chr(8192) || '-' || chr(8202) || chr(8232) || chr(8233) || chr(8239) || chr(8287) || chr(12288) || ']+'
        -- normalize-class-end
        , ' ', 'g'), ' ') AS display) AS d
    CROSS JOIN LATERAL (SELECT lower(d.display) AS norm) AS n
    WHERE d.display <> ''
    ORDER BY t.project_id, n.norm, t.precedence, d.display
)
INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
SELECT project_id, display, norm, kind, 'user'
FROM normalized_terms
ON CONFLICT (project_id, source, normalized_keyword) DO NOTHING;

ALTER TABLE project_business_profile
DROP COLUMN branded_keywords,
DROP COLUMN non_branded_keywords,
DROP COLUMN target_keywords;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE project_business_profile
ADD COLUMN branded_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,
ADD COLUMN non_branded_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,
ADD COLUMN target_keywords JSONB NOT NULL DEFAULT '[]'::jsonb;

WITH unioned AS (
    SELECT DISTINCT ON (pk.project_id, pk.normalized_keyword)
        pk.project_id, pk.keyword, pk.normalized_keyword, pk.kind
    FROM project_keywords AS pk
    ORDER BY pk.project_id, pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END
),
aggregated AS (
    SELECT
        project_id,
        COALESCE(jsonb_agg(keyword ORDER BY normalized_keyword) FILTER (WHERE kind = 'brand'), '[]'::jsonb) AS branded,
        COALESCE(jsonb_agg(keyword ORDER BY normalized_keyword) FILTER (WHERE kind = 'non_brand'), '[]'::jsonb) AS non_branded,
        COALESCE(jsonb_agg(keyword ORDER BY normalized_keyword), '[]'::jsonb) AS target
    FROM unioned
    GROUP BY project_id
)
UPDATE project_business_profile AS p
SET branded_keywords = a.branded,
    non_branded_keywords = a.non_branded,
    target_keywords = a.target
FROM aggregated AS a
WHERE a.project_id = p.project_id;

DROP TABLE IF EXISTS project_keywords;
-- +goose StatementEnd
