ALTER TABLE project
    DROP CONSTRAINT IF EXISTS project_instructions_size,
    ADD CONSTRAINT project_instructions_size CHECK (octet_length(instructions) <= 32768) NOT VALID;
