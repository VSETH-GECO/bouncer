CREATE TABLE login_logs
(
    id         INTEGER      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    username   varchar(255) NULL,
    mac        varchar(255) NULL,
    KEY idx_date (`created_at`),
    KEY idx_name (`username`),
    KEY idx_mac (`mac`)
);
