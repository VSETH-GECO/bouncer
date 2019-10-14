CREATE TABLE bouncer_election
(
    id         INTEGER     NOT NULL AUTO_INCREMENT PRIMARY KEY,
    time       TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    nodeid     varchar(64) NULL,
    KEY idx_date (`time`),
    KEY idx_nodeid (`nodeid`)
);