ALTER TABLE project
    ADD COLUMN instructions TEXT,
    ADD CONSTRAINT project_instructions_size CHECK (octet_length(instructions) <= 32768) NOT VALID;
