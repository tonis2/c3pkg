-- Which renderer version produced readme_html. Rendered READMEs are stored, so when the renderer
-- changes (pkg.RenderVersion) the stored copies are re-rendered from readme_md on the next start.
ALTER TABLE packages ADD COLUMN readme_render INTEGER NOT NULL DEFAULT 0;
