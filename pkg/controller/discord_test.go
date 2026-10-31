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

// card fetches the user card for search with enabled buttons and fails the test on error.
func (h *harness) card(search string, longCard bool) *UserCard {
	h.t.Helper()
	return h.cardWith(search, CardOptions{Buttons: true, Long: longCard})
}

// cardWith fetches the user card for search with the given options and fails the test on error.
func (h *harness) cardWith(search string, opts CardOptions) *UserCard {
	h.t.Helper()
	c, err := h.dc.GetDiscordUserCard(h.ctx, search, opts)
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

// component returns the component whose CustomID parses to a CardID with the given action inside the card's action
// rows, together with that CardID, or nil. It fails the test on a CustomID that does not parse.
func component(t *testing.T, c *UserCard, action CardAction) (discordgo.MessageComponent, CardID) {
	t.Helper()
	parse := func(s string) CardID {
		t.Helper()
		id, err := ParseCardID(s)
		if err != nil {
			t.Fatalf("component CustomID: %v", err)
		}
		return id
	}
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
				if id := parse(v.CustomID); id.Action == action {
					return v, id
				}
			case discordgo.SelectMenu:
				if id := parse(v.CustomID); id.Action == action {
					return v, id
				}
			}
		}
	}
	return nil, CardID{}
}

// has reports whether the card has a control with the given action.
func has(t *testing.T, c *UserCard, action CardAction) bool {
	t.Helper()
	comp, _ := component(t, c, action)
	return comp != nil
}

// wantButton checks that the card has an enabled button with the given action carrying m, and returns its CardID.
func wantButton(t *testing.T, c *UserCard, action CardAction, m mac.MAC) CardID {
	t.Helper()
	comp, id := component(t, c, action)
	b, ok := comp.(discordgo.Button)
	if !ok {
		t.Errorf("no button %q", action)
		return id
	}
	if b.Disabled {
		t.Errorf("button %q is disabled", action)
	}
	if id.MAC != m {
		t.Errorf("button %q carries MAC %s, want %s", action, id.MAC, m)
	}
	return id
}

// vlanMenu returns the card's VLAN menu and checks that it carries m.
func vlanMenu(t *testing.T, c *UserCard, m mac.MAC) discordgo.SelectMenu {
	t.Helper()
	comp, id := component(t, c, ActionSelectVLAN)
	menu, ok := comp.(discordgo.SelectMenu)
	if !ok {
		t.Fatal("no VLAN menu")
	}
	if id.MAC != m {
		t.Errorf("VLAN menu carries MAC %s, want %s", id.MAC, m)
	}
	return menu
}

// wantDefault checks that exactly the option with value want is the menu's default.
func wantDefault(t *testing.T, menu discordgo.SelectMenu, want string) {
	t.Helper()
	for _, o := range menu.Options {
		if isWant := o.Value == want; o.Default != isWant {
			t.Errorf("option %s Default = %v, want %v", o.Value, o.Default, isWant)
		}
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
	wantField(t, c, "Email", "N/A")
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

	menu := vlanMenu(t, c, fc.m)
	if menu.Disabled {
		t.Error("VLAN menu is disabled")
	}
	if len(menu.Options) != 2 {
		t.Fatalf("VLAN options = %d, want 2", len(menu.Options))
	}
	wantDefault(t, menu, "10")
	for _, a := range []CardAction{ActionLogout, ActionCancel} {
		wantButton(t, c, a, fc.m)
	}
	if id := wantButton(t, c, ActionChange, fc.m); id.VLAN != 0 {
		t.Errorf("change button VLAN = %d, want 0 (nothing selected)", id.VLAN)
	}
	if has(t, c, ActionLogin) {
		t.Error("login button present for a logged-in user")
	}
}

func TestUserCard_SelectedVLAN(t *testing.T) {
	h := newHarness(t)
	fc := h.fullClient(h.testSwitch())

	c := h.cardWith(fc.m.String(), CardOptions{Buttons: true, Long: true, SelectedVLAN: 20})
	wantDefault(t, vlanMenu(t, c, fc.m), "20")
	if id := wantButton(t, c, ActionChange, fc.m); id.VLAN != 20 {
		t.Errorf("change button VLAN = %d, want 20", id.VLAN)
	}

	// A VLAN the switch does not have leaves the primary VLAN chosen in the menu.
	c = h.cardWith(fc.m.String(), CardOptions{Buttons: true, Long: true, SelectedVLAN: 30})
	wantDefault(t, vlanMenu(t, c, fc.m), "10")
}

func TestUserCard_DisabledControls(t *testing.T) {
	h := newHarness(t)
	fc := h.fullClient(h.testSwitch())

	c := h.cardWith(fc.m.String(), CardOptions{Long: true})
	if !vlanMenu(t, c, fc.m).Disabled {
		t.Error("VLAN menu is enabled")
	}
	for _, a := range []CardAction{ActionLogout, ActionChange, ActionCancel} {
		if b, _ := component(t, c, a); b == nil || !b.(discordgo.Button).Disabled {
			t.Errorf("button %q missing or enabled", a)
		}
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
	h.p.Session(func(r *model.Radacct) { r.Username = m.Hex() }, onSwitch(sw.IPs[0].IP, "GigabitEthernet1/0/8"))

	c := h.card(m.String(), true)
	wantField(t, c, "Logged in", "❌")
	wantButton(t, c, ActionLogin, m)
	if has(t, c, ActionLogout) {
		t.Error("logout button present for a user who is not logged in")
	}
	vlanMenu(t, c, m)
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

	// Without a lease or a session the MAC is found through the login log, by the username or the MAC in any form.
	for _, search := range []string{email, m.Hex(), m.String(), strings.ToUpper(strings.ReplaceAll(m.String(), ":", "-"))} {
		c := h.card(search, false)
		if c.RealMAC != m.Hex() {
			t.Fatalf("search %q: RealMAC = %q, want %q", search, c.RealMAC, m.Hex())
		}
		wantField(t, c, "Username", "N/A")
		wantField(t, c, "Email", email)
	}
	u, err := loadUser(h.ctx, h.tx, m)
	if err != nil {
		t.Fatalf("loadUser: %v", err)
	}
	if u.Name != "N/A" || u.Email != email {
		t.Errorf("loadUser Name, Email = %q, %q, want %q, %q", u.Name, u.Email, "N/A", email)
	}
}

func TestUserCard_SessionOnlyClient(t *testing.T) {
	h := newHarness(t)
	sw := h.testSwitch()
	m := h.client()
	ip := h.p.IPv4().String()
	h.p.Session(func(r *model.Radacct) { r.Username, r.Framedipaddress = m.Hex(), ip },
		onSwitch(sw.IPs[0].IP, "GigabitEthernet1/0/10"))

	for _, search := range []string{m.String(), ip} {
		c := h.card(search, true)
		if c.RealMAC != m.Hex() {
			t.Errorf("search %q: RealMAC = %q, want %q", search, c.RealMAC, m.Hex())
			continue
		}
		wantField(t, c, "MAC", m.String())
		wantField(t, c, "Switch port", "GigabitEthernet1/0/10")
		wantButton(t, c, ActionLogin, m)
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
