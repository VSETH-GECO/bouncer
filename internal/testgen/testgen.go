// Package testgen builds random, fully valid rows for the tables bouncer touches, for tests.
//
// A generator comes in two flavours. Persistent inserts every row it builds through a db.Querier (normally the
// transaction from testdb.Tx) and returns it with its auto-increment ID filled in. Local builds the same rows but
// inserts nothing, so their IDs stay 0; it suits unit tests that only need realistic values.
//
// Every row method starts from a random row shaped like production data and then applies the caller's mods in
// order, so a test states only the fields it cares about:
//
//	p := testgen.Persistent(t, tx)
//	lease := p.Lease4(func(l *model.Lease4) { l.Hostname = &name })
//
// Randomness is deterministic per test: the seed is the FNV-1a 64-bit hash of t.Name(), so a test sees the same
// values on every run, and two generators in the same test produce the same stream. Setting the TESTGEN_SEED
// environment variable to a decimal uint64 overrides the seed; each generator logs the seed it uses. Uniqueness
// within a test comes from the width of the random fields, not from counters.
package testgen

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// SeedEnv is the environment variable that, set to a decimal uint64, overrides the per-test seed.
const SeedEnv = "TESTGEN_SEED"

// Gen generates random test rows. Create one with Persistent or Local.
type Gen struct {
	t   testing.TB
	q   db.Querier // nil for Local
	rnd *rand.Rand
}

// Persistent returns a generator whose row methods insert every row through q.
func Persistent(t testing.TB, q db.Querier) *Gen {
	t.Helper()
	if q == nil {
		t.Fatal("testgen: Persistent needs a non-nil Querier")
	}
	return newGen(t, q)
}

// Local returns a generator whose row methods build the same rows as Persistent but insert nothing; the
// auto-increment IDs of the returned rows stay 0.
func Local(t testing.TB) *Gen {
	t.Helper()
	return newGen(t, nil)
}

func newGen(t testing.TB, q db.Querier) *Gen {
	t.Helper()
	h := fnv.New64a()
	h.Write([]byte(t.Name()))
	seed := h.Sum64()
	if s := os.Getenv(SeedEnv); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("testgen: %s=%q is not a decimal uint64: %v", SeedEnv, s, err)
		}
		seed = v
	}
	t.Logf("testgen: seed %d (set %s to reproduce)", seed, SeedEnv)
	return &Gen{t: t, q: q, rnd: rand.New(rand.NewPCG(seed, 0))}
}

// Rand returns the generator's random stream, for tests that need further values from it.
func (g *Gen) Rand() *rand.Rand { return g.rnd }

// exec inserts through g.q and returns LastInsertId. It is a no-op returning 0 for a Local generator.
func (g *Gen) exec(tbl string, stmt mysql.Statement) int64 {
	g.t.Helper()
	if g.q == nil {
		return 0
	}
	res, err := stmt.ExecContext(context.Background(), g.q)
	if err != nil {
		g.t.Fatalf("testgen: inserting into %s: %v", tbl, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		g.t.Fatalf("testgen: reading insert ID of %s: %v", tbl, err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// hex returns n random bytes as 2n lowercase hex digits.
func (g *Gen) hex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.rnd.Uint32())
	}
	return hex.EncodeToString(b)
}

// between returns a random int in [lo, hi].
func (g *Gen) between(lo, hi int) int { return lo + g.rnd.IntN(hi-lo+1) }

// now is the current instant at DATETIME precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// future returns a random instant between 1h and 24h from now.
func (g *Gen) future() time.Time {
	return now().Add(time.Hour + time.Duration(g.rnd.Int64N(int64(23*time.Hour)))).Truncate(time.Second)
}

// MAC returns a MAC of 6 random bytes.
func (g *Gen) MAC() mac.MAC {
	var m mac.MAC
	for i := range m {
		m[i] = byte(g.rnd.Uint32())
	}
	return m
}

// IPv4 returns a random host address in 10.0.0.0/8 (never the network or broadcast address).
func (g *Gen) IPv4() net.IP {
	host := 1 + g.rnd.Uint32N(1<<24-2)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], 10<<24|host)
	return net.IPv4(b[0], b[1], b[2], b[3])
}

// IPv6 returns a random host address in fd00::/64 (never the all-zero interface ID).
func (g *Gen) IPv6() net.IP {
	ip := make(net.IP, net.IPv6len)
	ip[0] = 0xfd
	for {
		iid := g.rnd.Uint64()
		if iid != 0 {
			binary.BigEndian.PutUint64(ip[8:], iid)
			return ip
		}
	}
}

// Hostname returns a short random label followed by kea.HostnameSuffix, the form Kea stores.
func (g *Gen) Hostname() string { return "host-" + g.hex(4) + kea.HostnameSuffix }

// Time returns a random instant within 24 hours of now, in UTC and truncated to seconds (DATETIME precision).
func (g *Gen) Time() time.Time {
	d := time.Duration(g.rnd.Int64N(int64(48*time.Hour))) - 24*time.Hour
	return time.Now().Add(d).UTC().Truncate(time.Second)
}

// Lease4 builds an active Kea IPv4 lease, applies mods in order and inserts it. lease4 has no auto-increment key.
func (g *Gen) Lease4(mods ...func(*model.Lease4)) model.Lease4 {
	g.t.Helper()
	hw := g.MAC().Bytes()
	row := model.Lease4{
		Address:       binary.BigEndian.Uint32(g.IPv4().To4()),
		Hwaddr:        &hw,
		ValidLifetime: ptr(uint32(3600)),
		Expire:        ptr(g.future()),
		SubnetID:      ptr(uint32(1)),
		FqdnFwd:       ptr(false),
		FqdnRev:       ptr(false),
		Hostname:      ptr(g.Hostname()),
		State:         ptr(uint32(0)),
	}
	for _, mod := range mods {
		mod(&row)
	}
	g.exec("lease4", table.Lease4.INSERT(table.Lease4.AllColumns).MODEL(row))
	return row
}

// Lease6 builds an active Kea IPv6 address lease, applies mods in order and inserts it. lease6 has no
// auto-increment key.
func (g *Gen) Lease6(mods ...func(*model.Lease6)) model.Lease6 {
	g.t.Helper()
	addr := []byte(g.IPv6().To16())
	hw := g.MAC().Bytes()
	row := model.Lease6{
		Address:       addr,
		ValidLifetime: ptr(uint32(3600)),
		Expire:        ptr(g.future()),
		SubnetID:      ptr(uint32(1)),
		PrefLifetime:  ptr(uint32(1800)),
		LeaseType:     ptr(int8(0)),
		Iaid:          ptr(g.rnd.Uint32()),
		PrefixLen:     ptr(uint8(128)),
		FqdnFwd:       ptr(false),
		FqdnRev:       ptr(false),
		Hostname:      ptr(g.Hostname()),
		Hwaddr:        &hw,
		Hwtype:        ptr(uint16(1)),
		HwaddrSource:  ptr(uint32(0)),
		State:         ptr(uint32(0)),
	}
	for _, mod := range mods {
		mod(&row)
	}
	g.exec("lease6", table.Lease6.INSERT(table.Lease6.AllColumns).MODEL(row))
	return row
}

// Session builds an active FreeRADIUS accounting session (radacct row, no stop time), applies mods in order,
// inserts it and returns it with Radacctid filled in.
func (g *Gen) Session(mods ...func(*model.Radacct)) model.Radacct {
	g.t.Helper()
	n := now()
	row := model.Radacct{
		Acctsessionid:    g.hex(8),
		Acctuniqueid:     g.hex(16),
		Username:         g.MAC().Hex(),
		Nasipaddress:     g.IPv4().String(),
		Nasportid:        ptr(fmt.Sprintf("GigabitEthernet1/0/%d", g.between(1, 48))),
		Nasporttype:      ptr("Ethernet"),
		Acctstarttime:    ptr(n.Add(-time.Hour)),
		Acctupdatetime:   ptr(n),
		Acctsessiontime:  ptr(uint32(3600)),
		Acctinputoctets:  ptr(g.rnd.Int64N(1 << 40)),
		Acctoutputoctets: ptr(g.rnd.Int64N(1 << 40)),
		Calledstationid:  g.MAC().String(),
		Callingstationid: g.MAC().String(),
		Framedipaddress:  g.IPv4().String(),
	}
	for _, mod := range mods {
		mod(&row)
	}
	row.Radacctid = g.exec("radacct", table.Radacct.INSERT(table.Radacct.MutableColumns).MODEL(row))
	return row
}

// LoginLog builds a login_logs row, applies mods in order, inserts it and returns it with ID filled in.
func (g *Gen) LoginLog(mods ...func(*model.LoginLogs)) model.LoginLogs {
	g.t.Helper()
	at := g.Time()
	row := model.LoginLogs{
		CreatedAt: at,
		UpdatedAt: at,
		Username:  ptr("user-" + g.hex(4)),
		Mac:       ptr(g.MAC().Hex()),
	}
	for _, mod := range mods {
		mod(&row)
	}
	row.ID = int32(g.exec("login_logs", table.LoginLogs.INSERT(table.LoginLogs.MutableColumns).MODEL(row)))
	return row
}

// Job builds a pending bouncer_jobs row, applies mods in order, inserts it and returns it with ID filled in.
func (g *Gen) Job(mods ...func(*model.BouncerJobs)) model.BouncerJobs {
	g.t.Helper()
	row := model.BouncerJobs{
		ClientMAC:  g.MAC().Hex(),
		TargetVLAN: int16(g.between(2, 4094)),
		LastUpdate: now(),
	}
	for _, mod := range mods {
		mod(&row)
	}
	row.ID = int32(g.exec("bouncer_jobs", table.BouncerJobs.INSERT(table.BouncerJobs.MutableColumns).MODEL(row)))
	return row
}

// LogEntry builds a bouncer_log row of a VLAN change, applies mods in order, inserts it and returns it with ID
// filled in.
func (g *Gen) LogEntry(mods ...func(*model.BouncerLog)) model.BouncerLog {
	g.t.Helper()
	row := model.BouncerLog{
		ClientMAC:  g.MAC().Hex(),
		Date:       now(),
		OldVLAN:    int16(g.between(2, 4094)),
		NewVLAN:    int16(g.between(2, 4094)),
		SwitchIP:   ptr(g.IPv4().String()),
		SwitchPort: ptr(fmt.Sprintf("GigabitEthernet1/0/%d", g.between(1, 48))),
		ClientName: ptr(g.Hostname()),
		ClientIP:   ptr(g.IPv4().String()),
	}
	for _, mod := range mods {
		mod(&row)
	}
	row.ID = int32(g.exec("bouncer_log", table.BouncerLog.INSERT(table.BouncerLog.MutableColumns).MODEL(row)))
	return row
}

// fillVLAN gives every zero field of v a random value.
func (g *Gen) fillVLAN(v *model.BouncerVlan) {
	if v.Name == "" {
		v.Name = "vlan-" + g.hex(4)
	}
	if v.Description == "" {
		v.Description = "desc-" + g.hex(4)
	}
	if v.VlanID == 0 {
		v.VlanID = int32(g.between(2, 4094))
	}
	if v.IPRange == "" {
		v.IPRange = fmt.Sprintf("10.%d.%d.0/24", g.rnd.IntN(256), g.rnd.IntN(256))
	}
}

// VLAN builds an event VLAN (bouncer_vlan row), applies mods in order, inserts it and returns it with ID filled in.
func (g *Gen) VLAN(mods ...func(*model.BouncerVlan)) model.BouncerVlan {
	g.t.Helper()
	var row model.BouncerVlan
	g.fillVLAN(&row)
	for _, mod := range mods {
		mod(&row)
	}
	g.insertVLAN(&row)
	return row
}

func (g *Gen) insertVLAN(v *model.BouncerVlan) {
	g.t.Helper()
	v.ID = int32(g.exec("bouncer_vlan", table.BouncerVlan.INSERT(table.BouncerVlan.MutableColumns).MODEL(*v)))
}

// Authorized writes exactly the rows freeradius.Authorize writes for m on vlan: the radcheck password row and the
// radreply rows for the VLAN, tunnel medium type and tunnel type. It builds them from the freeradius constants
// rather than calling Authorize, so tests of Authorize stay independent of it.
func (g *Gen) Authorized(m mac.MAC, vlan int) {
	g.t.Helper()
	user := m.Hex()
	g.exec("radcheck", table.Radcheck.INSERT(table.Radcheck.MutableColumns).MODEL(model.Radcheck{
		Username: user, Attribute: freeradius.AttrPassword, Op: freeradius.OpSet, Value: user,
	}))
	for _, r := range []model.Radreply{
		{Username: user, Attribute: freeradius.AttrVLAN, Op: freeradius.OpSet, Value: strconv.Itoa(vlan)},
		{Username: user, Attribute: freeradius.AttrTunnelMediumType, Op: freeradius.OpSet, Value: freeradius.MediumType},
		{Username: user, Attribute: freeradius.AttrTunnelType, Op: freeradius.OpSet, Value: freeradius.TunnelType},
	} {
		g.exec("radreply", table.Radreply.INSERT(table.Radreply.MutableColumns).MODEL(r))
	}
}

// Switch is a switch with its management IPs and event VLANs, as Gen.Switch builds and inserts it.
type Switch struct {
	// Map is the bouncer_switch_map row.
	Map model.BouncerSwitchMap
	// IPs are the switch's bouncer_switch_ip rows; Gen.Switch sets their SwitchID.
	IPs []model.BouncerSwitchIP
	// VLANs are the event VLANs linked to the switch through bouncer_vlan_switch.
	VLANs []model.BouncerVlan
}

// Switch builds a switch with one random IP and one random VLAN that is also its primary VLAN, applies mods in
// order, then inserts the VLAN rows, the map row, the IP rows (with SwitchID filled in) and one bouncer_vlan_switch
// link per VLAN, and returns everything with IDs filled in.
//
// VLAN rows a mod added with a zero VlanID or an empty Name, Description or IPRange get random values for those
// fields, and IP rows with an empty IP get a random one. Mods may set Map.PrimaryVlan to a VLAN that is not among
// VLANs; Switch does not enforce that relation.
func (g *Gen) Switch(mods ...func(*Switch)) Switch {
	g.t.Helper()
	var v model.BouncerVlan
	g.fillVLAN(&v)
	s := Switch{
		Map: model.BouncerSwitchMap{
			PrimaryVlan: v.VlanID,
			Hostname:    "sw-" + g.hex(4),
			Location:    ptr(fmt.Sprintf("room-%d", g.between(1, 999))),
		},
		IPs:   []model.BouncerSwitchIP{{IP: g.IPv4().String()}},
		VLANs: []model.BouncerVlan{v},
	}
	for _, mod := range mods {
		mod(&s)
	}
	for i := range s.VLANs {
		g.fillVLAN(&s.VLANs[i])
	}
	for i := range s.IPs {
		if s.IPs[i].IP == "" {
			s.IPs[i].IP = g.IPv4().String()
		}
	}

	for i := range s.VLANs {
		g.insertVLAN(&s.VLANs[i])
	}
	s.Map.ID = int32(g.exec("bouncer_switch_map",
		table.BouncerSwitchMap.INSERT(table.BouncerSwitchMap.MutableColumns).MODEL(s.Map)))
	for i := range s.IPs {
		ip := &s.IPs[i]
		ip.SwitchID = s.Map.ID
		ip.ID = int32(g.exec("bouncer_switch_ip",
			table.BouncerSwitchIP.INSERT(table.BouncerSwitchIP.MutableColumns).MODEL(*ip)))
	}
	for _, v := range s.VLANs {
		g.exec("bouncer_vlan_switch", table.BouncerVlanSwitch.INSERT(table.BouncerVlanSwitch.MutableColumns).
			MODEL(model.BouncerVlanSwitch{EventVlanID: v.ID, SwitchID: s.Map.ID}))
	}
	return s
}
