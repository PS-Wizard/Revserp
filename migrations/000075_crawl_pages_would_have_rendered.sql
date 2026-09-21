-- +goose Up
-- would_have_rendered records that the JS render detector wanted a headless
-- render for this page, whether or not the render fallback actually ran. It
-- makes a disabled fallback measurable: the crawler can score how many pages
-- would have needed a render before the fallback is switched on for a crawl.
ALTER TABLE crawl_pages
    ADD COLUMN would_have_rendered BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS would_have_rendered;
