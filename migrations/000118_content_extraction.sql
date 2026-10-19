-- +goose Up
-- content_extractor records which main-content engine produced the stored
-- visible_text/content_blocks/word_count; content_extraction_status records
-- whether that attempt succeeded ('ok'), found no main content ('empty'), or
-- failed ('failed'). Both stay NULL for rows written before the extractor
-- landed (legacy) and for non-HTML/fetch-failed rows (no native extraction
-- attempt). There is deliberately no backfill: old rows must not be marked as
-- new extractions, and the 304 reuse path only copies forward rows the
-- current engine extracted successfully.
ALTER TABLE crawl_pages
    ADD COLUMN content_extractor TEXT,
    ADD COLUMN content_extraction_status TEXT CHECK (content_extraction_status IN ('ok', 'empty', 'failed'));

-- +goose Down
ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS content_extraction_status,
    DROP COLUMN IF EXISTS content_extractor;
