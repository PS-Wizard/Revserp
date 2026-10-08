-- +goose Up
-- +goose StatementBegin
-- The 1-5 Maps query product limit is gone. query_index widens from SMALLINT to
-- INTEGER so a run can carry any query count whose total cost still fits the
-- INTEGER credit columns; the check keeps only the non-negative floor.
DO $$
DECLARE fk record;
BEGIN
    FOR fk IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'local_visibility_results'::regclass
          AND contype = 'f'
          AND confrelid = 'local_run_cells'::regclass
    LOOP
        EXECUTE format('ALTER TABLE local_visibility_results DROP CONSTRAINT %I', fk.conname);
    END LOOP;
END $$;
ALTER TABLE local_run_cells ALTER COLUMN query_index TYPE INTEGER;
ALTER TABLE local_run_cells DROP CONSTRAINT local_run_cells_query_index_check;
ALTER TABLE local_run_cells ADD CONSTRAINT local_run_cells_query_index_check CHECK (query_index >= 0);
ALTER TABLE local_visibility_results ALTER COLUMN query_index TYPE INTEGER;
ALTER TABLE local_visibility_results ADD CONSTRAINT local_visibility_results_run_id_query_index_point_index_fkey
    FOREIGN KEY (run_id, query_index, point_index) REFERENCES local_run_cells(run_id, query_index, point_index) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Re-tightening to SMALLINT/five would orphan planned cells, so refuse rather
-- than silently drop run evidence.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM local_run_cells WHERE query_index > 4) THEN
        RAISE EXCEPTION 'local_run_cells holds runs with more than five queries; cannot restore the five-query cap';
    END IF;
END $$;
DO $$
DECLARE fk record;
BEGIN
    FOR fk IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'local_visibility_results'::regclass
          AND contype = 'f'
          AND confrelid = 'local_run_cells'::regclass
    LOOP
        EXECUTE format('ALTER TABLE local_visibility_results DROP CONSTRAINT %I', fk.conname);
    END LOOP;
END $$;
ALTER TABLE local_run_cells ALTER COLUMN query_index TYPE SMALLINT;
ALTER TABLE local_run_cells DROP CONSTRAINT local_run_cells_query_index_check;
ALTER TABLE local_run_cells ADD CONSTRAINT local_run_cells_query_index_check CHECK (query_index BETWEEN 0 AND 4);
ALTER TABLE local_visibility_results ALTER COLUMN query_index TYPE SMALLINT;
ALTER TABLE local_visibility_results ADD CONSTRAINT local_visibility_results_run_id_query_index_point_index_fkey
    FOREIGN KEY (run_id, query_index, point_index) REFERENCES local_run_cells(run_id, query_index, point_index) ON DELETE CASCADE;
-- +goose StatementEnd
