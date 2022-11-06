CREATE TABLE bouncer_discord_migration
(
    version    INTEGER      NOT NULL PRIMARY KEY,
    updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE bouncer_switch_map
(
    id           INTEGER    NOT NULL AUTO_INCREMENT PRIMARY KEY,
    primary_vlan INTEGER    NOT NULL UNIQUE,
    hostname     varchar(255) NOT NULL,
    location     varchar(255) NULL,
    KEY idx_vlan (`primary_vlan`)
);

CREATE TABLE bouncer_switch_ip
(
    id         INTEGER      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    switch_id  INTEGER      NOT NULL REFERENCES bouncer_switch_map(id),
    ip         varchar(255) NOT NULL,
    KEY idx_ip (`ip`),
    KEY idx_switch_id (`switch_id`)
);

CREATE TABLE bouncer_vlan
(
    id          INTEGER      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name        varchar(255) NOT NULL,
    description varchar(255) NOT NULL,
    vlan_id     INTEGER      NOT NULL UNIQUE,
    ip_range    varchar(255) NOT NULL UNIQUE,
    KEY idx_vlan_id (`vlan_id`),
    KEY idx_ip_range (`ip_range`),
    KEY idx_name (`name`)
);

CREATE TABLE bouncer_vlan_switch
(
    id             INTEGER   NOT NULL AUTO_INCREMENT PRIMARY KEY,
    event_vlan_id  INTEGER   NOT NULL REFERENCES bouncer_vlan(id),
    switch_id      INTEGER   NOT NULL REFERENCES bouncer_switch_map(id),
    KEY idx_event_vlan_id (`event_vlan_id`),
    KEY idx_switch_id (`switch_id`)
);