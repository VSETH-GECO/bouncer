CREATE TABLE bouncer_logout
(
    id              INTEGER     NOT NULL AUTO_INCREMENT PRIMARY KEY,
    scheduled_for   DATETIME    NOT NULL,
    state           varchar(16) NOT NULL,
    in_flight       TINYINT     NULL UNIQUE,
    last_warning    INTEGER     NULL,
    started_at      DATETIME    NULL,
    lockout_ends_at DATETIME    NULL,
    cancelled_by    varchar(32) NULL,
    coa_failures    INTEGER     NOT NULL DEFAULT 0,
    ignored_jobs    INTEGER     NOT NULL DEFAULT 0
);

CREATE TABLE bouncer_logout_approval
(
    logout_id   INTEGER     NOT NULL REFERENCES bouncer_logout (id),
    admin_id    varchar(32) NOT NULL,
    approved_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (logout_id, admin_id)
);
