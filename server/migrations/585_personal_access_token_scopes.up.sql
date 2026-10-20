ALTER TABLE personal_access_token
    ADD COLUMN scopes TEXT[] NOT NULL DEFAULT '{}'::text[];
