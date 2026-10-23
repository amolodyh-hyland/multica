ALTER TABLE project ADD COLUMN instructions TEXT
    CONSTRAINT project_instructions_size CHECK (octet_length(instructions) <= 32768);
