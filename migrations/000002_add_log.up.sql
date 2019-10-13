CREATE TABLE bouncer_log
(
    id         INTEGER     NOT NULL AUTO_INCREMENT PRIMARY KEY,
    clientMAC  varchar(32) NOT NULL,
    date       TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    oldVLAN    smallint    NOT NULL,
    newVLAN    smallint    NOT NULL,
    switchIP   varchar(25) NOT NULL,
    switchPort varchar(64) NOT NULL,
    KEY idx_date (`date`),
    KEY idx_switch (`switchIP`, `switchPort`),
    KEY idx_vlan (`newVLAN`)
);