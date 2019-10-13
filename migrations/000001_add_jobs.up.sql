CREATE TABLE bouncer_jobs
(
    id         INTEGER     NOT NULL AUTO_INCREMENT PRIMARY KEY,
    clientMAC  varchar(32) NOT NULL UNIQUE,
    targetVLAN smallint    NOT NULL,
    KEY idx_client (`clientMAC`),
    KEY idx_vlan (`targetVLAN`)
);