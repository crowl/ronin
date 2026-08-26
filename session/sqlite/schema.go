package sqlite

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    working_dir     TEXT NOT NULL,
    title           TEXT NOT NULL,
    parent_id       TEXT,
    model_provider  TEXT,
    model_name      TEXT,
    reasoning_level TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS sessions_by_working_dir
    ON sessions(working_dir, updated_at DESC);

CREATE TABLE IF NOT EXISTS session_events (
    session_id TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    type       TEXT NOT NULL,
    payload    BLOB NOT NULL,
    created_at INTEGER NOT NULL,

    PRIMARY KEY (session_id, seq),

    FOREIGN KEY (session_id)
        REFERENCES sessions(id)
        ON DELETE CASCADE
);
`
