-- An optional display name per source, and communities a person or
-- whole-instance source skips.
ALTER TABLE sources ADD COLUMN name TEXT;
ALTER TABLE sources ADD COLUMN exclude_communities TEXT;
