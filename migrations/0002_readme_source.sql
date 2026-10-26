-- Where a package's README came from: 'zip' (the README.md inside the latest .c3l, refreshed on
-- every publish) or 'manual' (uploaded by the owner, never overwritten by a publish).
ALTER TABLE packages ADD COLUMN readme_source TEXT NOT NULL DEFAULT 'zip';
UPDATE packages SET readme_source = 'manual' WHERE readme_md <> '';
