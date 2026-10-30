package jobs

import (
	"context"
	"fmt"
	"net"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
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
	if ip == nil {
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
