-- The privacy page text is now fixed; only its contact line is a setting.
-- An existing custom text keeps whatever followed its "## Questions" heading.
ALTER TABLE site_settings ADD COLUMN privacy_contact TEXT;
UPDATE site_settings
    SET privacy_contact = trim(substr(privacy_md, instr(privacy_md, '## Questions') + length('## Questions')), ' ' || char(10) || char(13))
    WHERE instr(privacy_md, '## Questions') > 0;
ALTER TABLE site_settings DROP COLUMN privacy_md;
