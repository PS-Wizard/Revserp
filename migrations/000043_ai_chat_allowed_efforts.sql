-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_features
ADD COLUMN ai_allowed_reasoning_efforts TEXT[] NOT NULL
DEFAULT ARRAY['none', 'low', 'high', 'max']::TEXT[],
ADD CONSTRAINT organization_features_ai_allowed_reasoning_efforts_check CHECK (
    cardinality(ai_allowed_reasoning_efforts) > 0
    AND array_ndims(ai_allowed_reasoning_efforts) = 1
    AND ai_allowed_reasoning_efforts <@ ARRAY['none', 'low', 'high', 'max']::TEXT[]
    AND cardinality(array_positions(ai_allowed_reasoning_efforts, 'none')) <= 1
    AND cardinality(array_positions(ai_allowed_reasoning_efforts, 'low')) <= 1
    AND cardinality(array_positions(ai_allowed_reasoning_efforts, 'high')) <= 1
    AND cardinality(array_positions(ai_allowed_reasoning_efforts, 'max')) <= 1
),
ADD CONSTRAINT organization_features_ai_monthly_message_limit_max_check
CHECK (ai_monthly_message_limit <= 1000000);

ALTER TABLE organization_features
DROP COLUMN ai_max_reasoning_effort;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_features
ADD COLUMN ai_max_reasoning_effort TEXT NOT NULL DEFAULT 'high'
CHECK (ai_max_reasoning_effort IN ('none', 'low', 'high', 'max'));

UPDATE organization_features
SET ai_max_reasoning_effort = CASE
    WHEN 'max' = ANY(ai_allowed_reasoning_efforts) THEN 'max'
    WHEN 'high' = ANY(ai_allowed_reasoning_efforts) THEN 'high'
    WHEN 'low' = ANY(ai_allowed_reasoning_efforts) THEN 'low'
    ELSE 'none'
END;

ALTER TABLE organization_features
DROP CONSTRAINT organization_features_ai_monthly_message_limit_max_check,
DROP CONSTRAINT organization_features_ai_allowed_reasoning_efforts_check,
DROP COLUMN ai_allowed_reasoning_efforts;
-- +goose StatementEnd
