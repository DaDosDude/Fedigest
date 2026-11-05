-- Only re-index a pick for search when its searchable text changes, not on
-- every update (comment counts, caches, pins and so on).
DROP TRIGGER IF EXISTS picks_au;
CREATE TRIGGER picks_au AFTER UPDATE OF title, excerpt, body ON picks BEGIN
    INSERT INTO picks_fts(picks_fts, rowid, title, excerpt, body) VALUES ('delete', old.id, old.title, old.excerpt, old.body);
    INSERT INTO picks_fts(rowid, title, excerpt, body) VALUES (new.id, new.title, new.excerpt, new.body);
END;
