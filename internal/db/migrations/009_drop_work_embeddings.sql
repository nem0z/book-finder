-- Search compares keyword vectors only (work_keyword_embeddings, computed by cmd/index), so the vectors of the
-- descriptions go, with their HNSW index. The migration stops with an error when a work with a description
-- vector has no keyword vector: dropping the table would make it unsearchable. Run cmd/index first (or, on
-- Azure, restore-db.ps1 from a database that has the keyword vectors).
DO $$
DECLARE
    missing bigint;
BEGIN
    SELECT count(*) INTO missing
    FROM work_embeddings e
    WHERE NOT EXISTS (SELECT 1 FROM work_keyword_embeddings k WHERE k.work_id = e.work_id);

    IF missing > 0 THEN
        RAISE EXCEPTION '% works with a description vector have no keyword vector: run cmd/index before this migration', missing;
    END IF;
END $$;

DROP TABLE work_embeddings;
