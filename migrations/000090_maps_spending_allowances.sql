-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_maps_credit_budgets ALTER COLUMN remaining_credits SET DEFAULT 500;
INSERT INTO organization_maps_credit_budgets(organization_id,remaining_credits)
SELECT id,500 FROM organizations
ON CONFLICT(organization_id) DO UPDATE SET remaining_credits = EXCLUDED.remaining_credits;

ALTER TABLE platform_maps_credit_budget ALTER COLUMN remaining_credits SET DEFAULT 5000;
UPDATE platform_maps_credit_budget SET remaining_credits = 5000 WHERE id = TRUE;

CREATE FUNCTION provision_organization_maps_allowance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO organization_maps_credit_budgets(organization_id) VALUES(NEW.id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER organization_maps_allowance_on_create AFTER INSERT ON organizations
    FOR EACH ROW EXECUTE FUNCTION provision_organization_maps_allowance();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER organization_maps_allowance_on_create ON organizations;
DROP FUNCTION provision_organization_maps_allowance();
ALTER TABLE organization_maps_credit_budgets ALTER COLUMN remaining_credits SET DEFAULT 0;
ALTER TABLE platform_maps_credit_budget ALTER COLUMN remaining_credits SET DEFAULT 0;
-- +goose StatementEnd
