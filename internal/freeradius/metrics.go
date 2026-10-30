package freeradius

import (
	"context"
	"fmt"
	"strconv"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/go-jet/jet/v2/mysql"
)

// VLANCount is a number of users on one VLAN.
type VLANCount struct{ VLAN, Count int }

// SwitchCount is a number of users on one switch, identified by its IP address.
type SwitchCount struct {
	SwitchIP string
	Count    int
}

func count(ctx context.Context, q db.Querier, stmt mysql.SelectStatement) (int, error) {
	var dest struct{ Count int }
	if err := stmt.QueryContext(ctx, q, &dest); err != nil {
		return 0, err
	}
	return dest.Count, nil
}

// CountKnown returns the number of distinct users that have ever had a session.
func CountKnown(ctx context.Context, q db.Querier) (int, error) {
	n, err := count(ctx, q, mysql.SELECT(mysql.COUNT(mysql.DISTINCT(table.Radacct.Username)).AS("count")).FROM(table.Radacct))
	if err != nil {
		return 0, fmt.Errorf("counting known users: %w", err)
	}
	return n, nil
}

// CountAuthorized returns the number of distinct authorized users.
func CountAuthorized(ctx context.Context, q db.Querier) (int, error) {
	n, err := count(ctx, q, mysql.SELECT(mysql.COUNT(mysql.DISTINCT(table.Radreply.Username)).AS("count")).
		FROM(table.Radreply).
		WHERE(table.Radreply.Attribute.EQ(mysql.String(AttrVLAN))))
	if err != nil {
		return 0, fmt.Errorf("counting authorized users: %w", err)
	}
	return n, nil
}

// CountActive returns the number of distinct users with an open session.
func CountActive(ctx context.Context, q db.Querier) (int, error) {
	n, err := count(ctx, q, mysql.SELECT(mysql.COUNT(mysql.DISTINCT(table.Radacct.Username)).AS("count")).
		FROM(table.Radacct).
		WHERE(table.Radacct.Acctstoptime.IS_NULL()))
	if err != nil {
		return 0, fmt.Errorf("counting active users: %w", err)
	}
	return n, nil
}

type vlanRow struct {
	Value string
	Count int
}

func vlanCounts(rows []vlanRow) ([]VLANCount, error) {
	out := make([]VLANCount, 0, len(rows))
	for _, r := range rows {
		vlan, err := strconv.Atoi(r.Value)
		if err != nil {
			return nil, fmt.Errorf("parsing VLAN %q: %w", r.Value, err)
		}
		out = append(out, VLANCount{VLAN: vlan, Count: r.Count})
	}
	return out, nil
}

// AuthorizedPerVLAN returns the number of distinct authorized users per VLAN.
func AuthorizedPerVLAN(ctx context.Context, q db.Querier) ([]VLANCount, error) {
	var rows []vlanRow
	err := mysql.SELECT(table.Radreply.Value.AS("vlan_row.value"), mysql.COUNT(mysql.DISTINCT(table.Radreply.Username)).AS("vlan_row.count")).
		FROM(table.Radreply).
		WHERE(table.Radreply.Attribute.EQ(mysql.String(AttrVLAN))).
		GROUP_BY(table.Radreply.Value).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("counting authorized users per VLAN: %w", err)
	}
	return vlanCounts(rows)
}

// ActivePerVLAN returns the number of distinct users with an open session per VLAN.
func ActivePerVLAN(ctx context.Context, q db.Querier) ([]VLANCount, error) {
	var rows []vlanRow
	err := mysql.SELECT(table.Radreply.Value.AS("vlan_row.value"), mysql.COUNT(mysql.DISTINCT(table.Radacct.Username)).AS("vlan_row.count")).
		FROM(table.Radacct.INNER_JOIN(table.Radreply, table.Radacct.Username.EQ(table.Radreply.Username))).
		WHERE(table.Radacct.Acctstoptime.IS_NULL().AND(table.Radreply.Attribute.EQ(mysql.String(AttrVLAN)))).
		GROUP_BY(table.Radreply.Value).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("counting active users per VLAN: %w", err)
	}
	return vlanCounts(rows)
}

// ActivePerSwitch returns the number of distinct users with an open session per switch.
func ActivePerSwitch(ctx context.Context, q db.Querier) ([]SwitchCount, error) {
	var rows []struct {
		SwitchIP string
		Count    int
	}
	err := mysql.SELECT(table.Radacct.Nasipaddress.AS("switch_ip"), mysql.COUNT(mysql.DISTINCT(table.Radacct.Username)).AS("count")).
		FROM(table.Radacct).
		WHERE(table.Radacct.Acctstoptime.IS_NULL()).
		GROUP_BY(table.Radacct.Nasipaddress).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("counting active users per switch: %w", err)
	}
	out := make([]SwitchCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, SwitchCount{SwitchIP: r.SwitchIP, Count: r.Count})
	}
	return out, nil
}
