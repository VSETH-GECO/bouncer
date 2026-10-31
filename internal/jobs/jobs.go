// Package jobs is bouncer's own work queue (bouncer_jobs).
package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// MaxRetries is the retry count at which a job stops being returned by Pending.
const MaxRetries = 3

// Job is a queued VLAN change. MAC is zero and RawMAC holds the stored string when the stored value does not parse.
type Job struct {
	ID         int
	MAC        mac.MAC
	RawMAC     string
	TargetVLAN int
	LastUpdate time.Time
	Retries    int
}

// Pending returns the jobs with fewer than MaxRetries retries, in id order.
func Pending(ctx context.Context, q db.Querier) ([]Job, error) {
	var rows []model.BouncerJobs
	err := mysql.SELECT(table.BouncerJobs.AllColumns).
		FROM(table.BouncerJobs).
		WHERE(table.BouncerJobs.Retires.LT(mysql.Int(MaxRetries))).
		ORDER_BY(table.BouncerJobs.ID.ASC()).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("querying pending jobs: %w", err)
	}

	list := make([]Job, 0, len(rows))
	for _, r := range rows {
		list = append(list, fromRow(r))
	}
	return list, nil
}

// Latest returns the newest job (highest id) of m whatever its retry count, so unlike Pending it includes jobs that
// ran out of retries. It returns an error wrapping db.ErrNotFound if m has no job.
func Latest(ctx context.Context, q db.Querier, m mac.MAC) (Job, error) {
	var row model.BouncerJobs
	err := mysql.SELECT(table.BouncerJobs.AllColumns).
		FROM(table.BouncerJobs).
		WHERE(table.BouncerJobs.ClientMAC.EQ(mysql.String(m.Hex()))).
		ORDER_BY(table.BouncerJobs.ID.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return Job{}, fmt.Errorf("looking up latest job of %s: %w", m.Hex(), db.NotFound(err))
	}
	return fromRow(row), nil
}

// fromRow converts a bouncer_jobs row, leaving MAC zero when the stored value does not parse.
func fromRow(r model.BouncerJobs) Job {
	j := Job{
		ID:         int(r.ID),
		RawMAC:     r.ClientMAC,
		TargetVLAN: int(r.TargetVLAN),
		LastUpdate: r.LastUpdate,
		Retries:    int(r.Retires),
	}
	if m, err := mac.Parse(r.ClientMAC); err == nil {
		j.MAC = m
	}
	return j
}

// minVLAN and maxVLAN bound the VLAN IDs usable on a port: 802.1Q reserves 0 and 4095.
const (
	minVLAN = 1
	maxVLAN = 4094
)

// Create queues a job moving the client to the given VLAN. It returns an error without touching the database
// when vlan is outside 1..4094.
func Create(ctx context.Context, q db.Querier, m mac.MAC, vlan int) error {
	if vlan < minVLAN || vlan > maxVLAN {
		return fmt.Errorf("creating job for %s: vlan %d outside %d..%d", m.Hex(), vlan, minVLAN, maxVLAN)
	}
	_, err := table.BouncerJobs.INSERT(table.BouncerJobs.ClientMAC, table.BouncerJobs.TargetVLAN).
		VALUES(m.Hex(), int16(vlan)).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("creating job for %s: %w", m.Hex(), err)
	}
	return nil
}

// Delete removes a job. Deleting a nonexistent id is not an error.
func Delete(ctx context.Context, q db.Querier, id int) error {
	_, err := table.BouncerJobs.DELETE().
		WHERE(table.BouncerJobs.ID.EQ(mysql.Int(int64(id)))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("deleting job %d: %w", id, err)
	}
	return nil
}

// SetRetries sets the retry counter of a job and refreshes its last_update to the database's current time.
func SetRetries(ctx context.Context, q db.Querier, id, retries int) error {
	_, err := table.BouncerJobs.UPDATE().
		SET(
			table.BouncerJobs.Retires.SET(mysql.Int(int64(retries))),
			table.BouncerJobs.LastUpdate.SET(mysql.CURRENT_TIMESTAMP()),
		).
		WHERE(table.BouncerJobs.ID.EQ(mysql.Int(int64(id)))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("updating retry counter of job %d: %w", id, err)
	}
	return nil
}
