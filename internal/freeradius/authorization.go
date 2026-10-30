// Package freeradius reads and writes the FreeRADIUS tables radacct, radcheck and radreply.
package freeradius

import (
	"context"
	"fmt"
	"strconv"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// Attribute names and values FreeRADIUS is told to use for an authorized client.
const (
	AttrPassword         = "Cleartext-Password"
	AttrVLAN             = "Tunnel-Private-Group-ID"
	AttrTunnelMediumType = "Tunnel-Medium-Type"
	AttrTunnelType       = "Tunnel-Type"

	MediumType = "IEEE-802"
	TunnelType = "VLAN"

	// OpSet is the FreeRADIUS operator that sets an attribute, replacing any earlier value.
	OpSet = ":="
)

// Authorize lets the client m onto the given VLAN, using its MAC as both username and password.
func Authorize(ctx context.Context, q db.Querier, m mac.MAC, vlan int) error {
	user := m.Hex()
	_, err := table.Radcheck.INSERT(table.Radcheck.Username, table.Radcheck.Attribute, table.Radcheck.Op, table.Radcheck.Value).
		MODEL(model.Radcheck{Username: user, Attribute: AttrPassword, Op: OpSet, Value: user}).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("inserting radcheck password: %w", err)
	}
	replies := []model.Radreply{
		{Username: user, Attribute: AttrVLAN, Op: OpSet, Value: strconv.Itoa(vlan)},
		{Username: user, Attribute: AttrTunnelMediumType, Op: OpSet, Value: MediumType},
		{Username: user, Attribute: AttrTunnelType, Op: OpSet, Value: TunnelType},
	}
	_, err = table.Radreply.INSERT(table.Radreply.Username, table.Radreply.Attribute, table.Radreply.Op, table.Radreply.Value).
		MODELS(replies).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("inserting radreply rows: %w", err)
	}
	return nil
}

// AssignedVLAN returns the VLAN m is assigned to. It returns an error wrapping db.ErrNotFound if m was never
// authorized.
func AssignedVLAN(ctx context.Context, q db.Querier, m mac.MAC) (int, error) {
	var dest model.Radreply
	err := mysql.SELECT(table.Radreply.Value).
		FROM(table.Radreply).
		WHERE(table.Radreply.Attribute.EQ(mysql.String(AttrVLAN)).AND(table.Radreply.Username.EQ(mysql.String(m.Hex())))).
		LIMIT(1).
		QueryContext(ctx, q, &dest)
	if err != nil {
		return 0, fmt.Errorf("looking up assigned VLAN: %w", db.NotFound(err))
	}
	vlan, err := strconv.Atoi(dest.Value)
	if err != nil {
		return 0, fmt.Errorf("parsing assigned VLAN %q: %w", dest.Value, err)
	}
	return vlan, nil
}

// SetVLAN moves an already authorized client to another VLAN.
func SetVLAN(ctx context.Context, q db.Querier, m mac.MAC, vlan int) error {
	_, err := table.Radreply.UPDATE(table.Radreply.Value).
		SET(mysql.String(strconv.Itoa(vlan))).
		WHERE(table.Radreply.Attribute.EQ(mysql.String(AttrVLAN)).AND(table.Radreply.Username.EQ(mysql.String(m.Hex())))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("updating radreply VLAN: %w", err)
	}
	return nil
}
