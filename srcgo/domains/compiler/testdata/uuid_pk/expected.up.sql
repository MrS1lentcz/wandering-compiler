BEGIN;

CREATE TABLE sessions (
    id UUID NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    client_key UUID NOT NULL DEFAULT gen_random_uuid(),
    token VARCHAR(64) NOT NULL,
    email VARCHAR(320) NOT NULL,
    note TEXT NOT NULL,
    CONSTRAINT sessions_token_blank CHECK (token <> ''),
    CONSTRAINT sessions_email_blank CHECK (email <> ''),
    CONSTRAINT sessions_email_format CHECK (email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
    CONSTRAINT sessions_note_len CHECK (char_length(note) >= 8 AND char_length(note) <= 4000),
    CONSTRAINT sessions_note_blank CHECK (note <> '')
);

COMMENT ON TABLE sessions IS 'Grand-tour fixture: UUID PK + UUID_V7 auto; UUID_V4 auto on a second
column; CHAR with max_len (VARCHAR); EMAIL (type-implied regex);
TEXT with explicit min_len + max_len (length CHECK with both bounds).
Exercises the pre-M10 attachChecks fix: UUID columns must not receive
a `CHECK (col <> '''')` synth (fails at apply on PG''s UUID type) and
the redundant UUID regex is no longer emitted.';

COMMIT;
