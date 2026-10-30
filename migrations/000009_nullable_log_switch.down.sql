UPDATE bouncer_log SET switchIP = '' WHERE switchIP IS NULL;
UPDATE bouncer_log SET switchPort = '' WHERE switchPort IS NULL;
ALTER TABLE bouncer_log MODIFY switchIP varchar(25) NOT NULL DEFAULT '', MODIFY switchPort varchar(64) NOT NULL DEFAULT '';
