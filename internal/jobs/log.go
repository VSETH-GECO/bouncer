package jobs

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// Result is the outcome of a VLAN change. Nil fields are logged as NULL.
type Result struct {
	MAC                  mac.MAC
	OldVLAN, NewVLAN     int
	SwitchIP             net.IP
	SwitchPort, Hostname *string
	ClientIP             net.IP
}

func ipString(ip net.IP) *string {
	if len(ip) == 0 {
		return nil
	}
	s := ip.String()
	return &s
}

// LogResult records the outcome of a job in bouncer_log.
func LogResult(ctx context.Context, q db.Querier, r Result) error {
	row := model.BouncerLog{
		ClientMAC:  r.MAC.Hex(),
		OldVLAN:    int16(r.OldVLAN),
		NewVLAN:    int16(r.NewVLAN),
		SwitchIP:   ipString(r.SwitchIP),
		SwitchPort: r.SwitchPort,
		ClientName: r.Hostname,
		ClientIP:   ipString(r.ClientIP),
	}
	_, err := table.BouncerLog.INSERT(
		table.BouncerLog.ClientMAC, table.BouncerLog.OldVLAN, table.BouncerLog.NewVLAN,
		table.BouncerLog.SwitchIP, table.BouncerLog.SwitchPort, table.BouncerLog.ClientName, table.BouncerLog.ClientIP,
	).MODEL(row).ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("logging result for %s: %w", r.MAC.Hex(), err)
	}
	return nil
}

// CountMovesSince returns how many moves to vlan bouncer_log recorded at or after since.
func CountMovesSince(ctx context.Context, q db.Querier, vlan int, since time.Time) (int, error) {
	var dest struct{ Count int }
	err := mysql.SELECT(mysql.COUNT(mysql.STAR).AS("count")).
		FROM(table.BouncerLog).
		WHERE(table.BouncerLog.NewVLAN.EQ(mysql.Int(int64(vlan))).
			AND(table.BouncerLog.Date.GT_EQ(mysql.TimestampT(since)))).
		QueryContext(ctx, q, &dest)
	if err != nil {
		return 0, fmt.Errorf("counting moves to VLAN %d: %w", vlan, err)
	}
	return dest.Count, nil
}
