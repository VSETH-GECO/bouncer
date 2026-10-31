package controller

import (
	"net"
	"strings"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/bwmarrin/discordgo"
)

// testSwitch inserts a switch with event VLANs 10 (primary) and 20 and one management IP.
func (h *harness) testSwitch() testgen.Switch {
	h.t.Helper()
	return h.p.Switch(func(s *testgen.Switch) {
		s.VLANs = []model.BouncerVlan{{VlanID: 10}, {VlanID: 20}}
		s.Map.PrimaryVlan = 10
	})
}

// login inserts a login log entry of m with the given username.
func (h *harness) login(m mac.MAC, username string) {
	h.t.Helper()
	h.p.LoginLog(func(l *model.LoginLogs) { l.Username, l.Mac = ptr(username), ptr(m.Hex()) })
}

// onSwitch is a session mod placing the session on the given switch IP and port.
func onSwitch(ip, port string) func(*model.Radacct) {
	return func(r *model.Radacct) { r.Nasipaddress, r.Nasportid = ip, ptr(port) }
}

// card fetches the user card for search and fails the test on error.
func (h *harness) card(search string, longCard bool) *UserCard {
	h.t.Helper()
	c, err := h.dc.GetDiscordUserCard(h.ctx, search, true, longCard)
	if err != nil {
		h.t.Fatalf("GetDiscordUserCard(%q): %v", search, err)
	}
	return c
}

// field returns the value of the card's embed field with the given name, and whether it exists.
func field(c *UserCard, name string) (string, bool) {
	for _, e := range c.Embeds {
		for _, f := range e.Fields {
			if f.Name == name {
				return f.Value, true
			}
		}
	}
	return "", false
}

// wantField checks that the card has an embed field name with value want.
func wantField(t *testing.T, c *UserCard, name, want string) {
	t.Helper()
	got, ok := field(c, name)
	if !ok {
		t.Errorf("no embed field %q", name)
	} else if got != want {
		t.Errorf("embed field %q = %q, want %q", name, got, want)
	}
}

// component returns the component with the given CustomID inside the card's action rows, or nil.
func component(c *UserCard, id string) discordgo.MessageComponent {
	for _, row := range c.Components {
		var inner []discordgo.MessageComponent
		switch r := row.(type) {
		case discordgo.ActionsRow:
			inner = r.Components
		case *discordgo.ActionsRow:
			inner = r.Components
		}
		for _, comp := range inner {
			switch v := comp.(type) {
			case discordgo.Button:
				if v.CustomID == id {
					return v
				}
			case discordgo.SelectMenu:
				if v.CustomID == id {
					return v
				}
			}
		}
	}
	return nil
}

// wantButton checks that the card has an enabled button with the given CustomID.
func wantButton(t *testing.T, c *UserCard, id string) {
	t.Helper()
	b, ok := component(c, id).(discordgo.Button)
	if !ok {
		t.Errorf("no button %q", id)
	} else if b.Disabled {
		t.Errorf("button %q is disabled", id)
	}
}

// fullClient is a client with a login, both leases, an active session on sw's port GigabitEthernet1/0/7 and VLAN 10.
type fullClient struct {
	m        mac.MAC
	hostname string
	ip, ip6  string
}

func (h *harness) fullClient(sw testgen.Switch) fullClient {
	h.t.Helper()
	m := h.client()
	h.login(m, "alice")
	l4 := h.lease4(m)
	l6 := h.lease6(m)
	h.p.Session(func(r *model.Radacct) { r.Username = m.Hex() }, onSwitch(sw.IPs[0].IP, "GigabitEthernet1/0/7"))
	h.p.Authorized(m, 10)
	return fullClient{m: m, hostname: *l4.Hostname, ip: kea4(l4.Address), ip6: net.IP(l6.Address).String()}
}

func TestUserCard_NotFound(t *testing.T) {
	h := newHarness(t)
	c := h.card("nobody-"+h.p.MAC().Hex(), true)
	if c.Content != "No user found with this search string" {
		t.Errorf("Content = %q, want the not-found message", c.Content)
	}
	if c.RealMAC != "" {
		t.Errorf("RealMAC = %q, want empty", c.RealMAC)
	}
	if len(c.Embeds) != 0 {
		t.Errorf("embeds = %d, want 0", len(c.Embeds))
	}
}

func TestUserCard_LongCardFull(t *testing.T) {
	h := newHarness(t)
	sw := h.testSwitch()
	fc := h.fullClient(sw)

	c := h.card(fc.m.String(), true)
	if c.RealMAC != fc.m.Hex() {
		t.Errorf("RealMAC = %q, want %q", c.RealMAC, fc.m.Hex())
	}
	wantField(t, c, "MAC", fc.m.String())
	wantField(t, c, "Hostname", fc.hostname)
	wantField(t, c, "IP", fc.ip)
	wantField(t, c, "IPv6", fc.ip6)
	wantField(t, c, "Username", "alice")
	if online, _ := field(c, "Online"); !strings.HasPrefix(online, "✅") {
		t.Errorf("Online = %q, want it to start with ✅", online)
	}
	wantField(t, c, "Logged in", "✅")
	wantField(t, c, "Switch name", sw.Map.Hostname)
	wantField(t, c, "Switch port", "GigabitEthernet1/0/7")
	wantField(t, c, "Switch location", *sw.Map.Location)
	if !strings.HasPrefix(c.Content, "Sessions in descending order") {
		t.Errorf("Content = %q, want the sessions table", c.Content)
	}

	menu, ok := component(c, "findVlanSelect").(discordgo.SelectMenu)
	if !ok {
		t.Fatal("no findVlanSelect menu")
	}
	if menu.Disabled {
		t.Error("findVlanSelect is disabled")
	}
	if len(menu.Options) != 2 {
		t.Fatalf("VLAN options = %d, want 2", len(menu.Options))
	}
	for _, o := range menu.Options {
		if want := o.Value == "10"; o.Default != want {
			t.Errorf("option %s Default = %v, want %v", o.Value, o.Default, want)
		}
	}
	for _, id := range []string{"findLogoutBtn", "findChangeBtn", "findCancelBtn"} {
		wantButton(t, c, id)
	}
	if component(c, "findLoginBtn") != nil {
		t.Error("findLoginBtn present for a logged-in user")
	}
}

func TestUserCard_ShortCard(t *testing.T) {
	h := newHarness(t)
	fc := h.fullClient(h.testSwitch())

	c := h.card(fc.m.String(), false)
	if c.Content != "User found, showing you their card" {
		t.Errorf("Content = %q", c.Content)
	}
	if len(c.Components) != 0 {
		t.Errorf("components = %d, want 0", len(c.Components))
	}
	if c.RealMAC != fc.m.Hex() {
		t.Errorf("RealMAC = %q, want %q", c.RealMAC, fc.m.Hex())
	}
}

func TestUserCard_NotLoggedInShowsLoginButton(t *testing.T) {
	h := newHarness(t)
	sw := h.testSwitch()
	m := h.client()
	h.lease4(m) // findMAC only searches the login log and the leases, not the sessions
	h.p.Session(func(r *model.Radacct) { r.Username = m.Hex() }, onSwitch(sw.IPs[0].IP, "GigabitEthernet1/0/8"))

	c := h.card(m.String(), true)
	wantField(t, c, "Logged in", "❌")
	wantButton(t, c, "findLoginBtn")
	if component(c, "findLogoutBtn") != nil {
		t.Error("findLogoutBtn present for a user who is not logged in")
	}
	if component(c, "findVlanSelect") == nil {
		t.Error("no findVlanSelect menu")
	}
}

func TestUserCard_UnknownSwitchHasNoVLANMenu(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.lease4(m)
	h.p.Session(func(r *model.Radacct) { r.Username = m.Hex() }, onSwitch(h.p.IPv4().String(), "GigabitEthernet1/0/9"))
	h.p.Authorized(m, 10)

	c := h.card(m.String(), true)
	if len(c.Components) != 0 {
		t.Errorf("components = %d, want 0 (no VLANs without a known switch)", len(c.Components))
	}
	if _, ok := field(c, "Switch name"); ok {
		t.Error("Switch name field present for an unknown switch")
	}
	wantField(t, c, "Logged in", "✅")
	if !strings.HasPrefix(c.Content, "Sessions in descending order") {
		t.Errorf("Content = %q, want the sessions table", c.Content)
	}
}

func TestUserCard_SearchByUsernameAndHostname(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.login(m, "bob")
	label := "bobs-pc-" + h.p.MAC().Hex()
	h.lease4(m, func(l *model.Lease4) { l.Hostname = ptr(label + kea.HostnameSuffix) })

	for _, search := range []string{"bob", "BOB", label} {
		if c := h.card(search, false); c.RealMAC != m.Hex() {
			t.Errorf("search %q: RealMAC = %q, want %q", search, c.RealMAC, m.Hex())
		}
	}
}

func TestUserCard_EmailUsername(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	const email = "carol@example.org"
	h.login(m, email)

	// Without a lease the MAC is found through the login log, which matches only the stored hex form.
	c := h.card(m.Hex(), false)
	if c.RealMAC != m.Hex() {
		t.Fatalf("RealMAC = %q, want %q", c.RealMAC, m.Hex())
	}
	wantField(t, c, "Username", "N/A")
	// The card has no email field: loadUser moves the address into User.Email, which nothing renders.
	for _, e := range c.Embeds {
		for _, f := range e.Fields {
			if strings.Contains(f.Value, email) {
				t.Errorf("embed field %q shows the email %q", f.Name, f.Value)
			}
		}
	}
	u, err := loadUser(h.ctx, h.tx, m)
	if err != nil {
		t.Fatalf("loadUser: %v", err)
	}
	if u.Name != "N/A" || u.Email != email {
		t.Errorf("loadUser Name, Email = %q, %q, want %q, %q", u.Name, u.Email, "N/A", email)
	}
}

func TestChangeUserVLAN(t *testing.T) {
	h := newHarness(t)
	dc := h.dc
	m, m2 := h.client(), h.client()

	if err := dc.ChangeUserVLAN(h.ctx, m, 20); err != nil {
		t.Fatalf("ChangeUserVLAN: %v", err)
	}
	if j := h.mustPending(m); j.TargetVLAN != 20 {
		t.Errorf("TargetVLAN = %d, want 20", j.TargetVLAN)
	}

	if err := dc.ChangeUserVLAN(h.ctx, m2, 70000); err == nil {
		t.Error("ChangeUserVLAN to VLAN 70000 succeeded, want an error")
	}
	if j := h.pendingJob(m2); j != nil {
		t.Errorf("job created for an invalid VLAN: %+v", j)
	}
}
