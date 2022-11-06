package database

import (
	"database/sql"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) GetDiscordVersion() (int, error) {
	rows, err := h.connection.Query("select version from bouncer_discord_migration order by version desc limit 1;")
	defer Close(rows)
	if err != nil {
		return -1, err
	}

	if !rows.Next() {
		log.Warn("No previous discord migration entry found, assuming a fresh start")
		return 0, nil
	}

	var value = 0
	err = rows.Scan(&value)
	return value, err
}

func (h *Handler) SetDiscordVersion(version int, tx *sql.Tx) error {
	_, err := tx.Exec("insert into bouncer_discord_migration(version, updated_at) values(?, CURRENT_TIMESTAMP())", version)
	return err
}
