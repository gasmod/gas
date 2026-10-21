CREATE TABLE notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    owner      TEXT     NOT NULL,
    title      TEXT     NOT NULL,
    body       TEXT     NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX notes_owner_idx ON notes (owner);
