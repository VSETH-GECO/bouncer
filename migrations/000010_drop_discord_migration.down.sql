CREATE TABLE bouncer_discord_migration
(
    version    INTEGER      NOT NULL PRIMARY KEY,
    updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
