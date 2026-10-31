package discord

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestRequestedVLAN(t *testing.T) {
	m := mac.MustParse("aa:bb:cc:dd:ee:01")
	for _, tt := range []struct {
		id         controller.CardID
		wantVLAN   int
		wantChange bool
	}{
		{controller.CardID{Action: controller.ActionLogout, MAC: m}, logoutVLAN, true},
		{controller.CardID{Action: controller.ActionChange, MAC: m, VLAN: 620}, 620, true},
		{controller.CardID{Action: controller.ActionChange, MAC: m}, 0, true},
		{controller.CardID{Action: controller.ActionLogin, MAC: m}, 0, true},
		{controller.CardID{Action: controller.ActionCancel, MAC: m}, 0, false},
		{controller.CardID{Action: controller.ActionSelectVLAN, MAC: m}, 0, false},
	} {
		vlan, change := requestedVLAN(tt.id)
		if vlan != tt.wantVLAN || change != tt.wantChange {
			t.Errorf("requestedVLAN(%s) = %d, %v, want %d, %v", tt.id, vlan, change, tt.wantVLAN, tt.wantChange)
		}
	}
}

func TestResolveVLAN(t *testing.T) {
	primary := &switches.VLAN{VLANID: 610}
	sw := switches.Switch{Hostname: "sw", VLANs: []switches.VLAN{*primary, {VLANID: 620}}, PrimaryVLAN: primary}
	for _, tt := range []struct{ in, want int }{{620, 620}, {610, 610}, {0, 610}, {1, 610}, {499, 610}} {
		if got, err := resolveVLAN(sw, tt.in); err != nil || got != tt.want {
			t.Errorf("resolveVLAN(%d) = %d, %v, want %d", tt.in, got, err, tt.want)
		}
	}
	sw.PrimaryVLAN = nil
	if got, err := resolveVLAN(sw, 620); err != nil || got != 620 {
		t.Errorf("without primary: resolveVLAN(620) = %d, %v, want 620", got, err)
	}
	if _, err := resolveVLAN(sw, 0); err == nil {
		t.Error("without primary: resolveVLAN(0) succeeded, want an error")
	}
}

func TestSelectedVLAN(t *testing.T) {
	if v, err := selectedVLAN([]string{"620"}); err != nil || v != 620 {
		t.Errorf("selectedVLAN([620]) = %d, %v", v, err)
	}
	for _, values := range [][]string{nil, {}, {"x"}, {"0"}, {"-3"}, {"10", "20"}} {
		if v, err := selectedVLAN(values); err == nil {
			t.Errorf("selectedVLAN(%q) = %d, want an error", values, v)
		}
	}
}

// fixture is a Discord over one rolled-back test transaction, with a logged-in client that has an active session on
// a switch with VLANs 610 (primary) and 620, and also 499 if requested. The VLAN IDs differ from those of the
// controller tests because bouncer_vlan.vlan_id is unique and test packages run concurrently against one database.
type fixture struct {
	t   *testing.T
	ctx context.Context
	tx  *sql.Tx
	p   *testgen.Gen
	d   *Discord
	m   mac.MAC
}

func newFixture(t *testing.T, with499 bool) *fixture {
	t.Helper()
	store := testdb.Store(t)
	p := testgen.Persistent(t, store.Tx())
	vlans := []model.BouncerVlan{{VlanID: 610}, {VlanID: 620}}
	if with499 {
		vlans = append(vlans, model.BouncerVlan{VlanID: 499})
	}
	sw := p.Switch(func(s *testgen.Switch) { s.VLANs, s.Map.PrimaryVlan = vlans, 610 })
	m := p.MAC()
	p.LoginLog(func(l *model.LoginLogs) { u, h := "dora", m.Hex(); l.Username, l.Mac = &u, &h })
	p.Session(func(r *model.Radacct) { r.Username, r.Nasipaddress = m.Hex(), sw.IPs[0].IP })
	p.Authorized(m, 610)
	return &fixture{
		t:   t,
		ctx: context.Background(),
		tx:  store.Tx(),
		p:   p,
		d:   &Discord{db: store, dc: controller.NewDiscordController(store)},
		m:   m,
	}
}

// pending returns the target VLAN of the pending job of the fixture's client, or 0 if there is none.
func (f *fixture) pending() int {
	f.t.Helper()
	all, err := jobs.Pending(f.ctx, f.tx)
	if err != nil {
		f.t.Fatalf("Pending: %v", err)
	}
	for _, j := range all {
		if j.MAC == f.m {
			return j.TargetVLAN
		}
	}
	return 0
}

// controls returns the controls of a response by their parsed CardID.
func controls(t *testing.T, resp *discordgo.InteractionResponse) map[controller.CardAction]struct {
	ID        controller.CardID
	Component discordgo.MessageComponent
} {
	t.Helper()
	out := map[controller.CardAction]struct {
		ID        controller.CardID
		Component discordgo.MessageComponent
	}{}
	for _, row := range resp.Data.Components {
		var inner []discordgo.MessageComponent
		switch r := row.(type) {
		case discordgo.ActionsRow:
			inner = r.Components
		case *discordgo.ActionsRow:
			inner = r.Components
		}
		for _, c := range inner {
			var customID string
			switch v := c.(type) {
			case discordgo.Button:
				customID = v.CustomID
			case discordgo.SelectMenu:
				customID = v.CustomID
			}
			id, err := controller.ParseCardID(customID)
			if err != nil {
				t.Fatalf("control CustomID: %v", err)
			}
			out[id.Action] = struct {
				ID        controller.CardID
				Component discordgo.MessageComponent
			}{id, c}
		}
	}
	return out
}

func wantMessage(t *testing.T, resp *discordgo.InteractionResponse, content string) {
	t.Helper()
	if resp.Type != discordgo.InteractionResponseChannelMessageWithSource || resp.Data.Content != content {
		t.Errorf("response = type %v %q, want a new message %q", resp.Type, resp.Data.Content, content)
	}
}

func TestUserCommandResponse(t *testing.T) {
	f := newFixture(t, false)

	resp, _ := f.d.userCommandResponse(f.ctx, "ab", true)
	wantMessage(t, resp, msgInvalidQuery)

	resp, _ = f.d.userCommandResponse(f.ctx, f.m.Hex(), false)
	if resp.Type != discordgo.InteractionResponseChannelMessageWithSource || len(resp.Data.Embeds) != 1 {
		t.Errorf("user command: type %v, %d embeds, want a new message with the card", resp.Type, len(resp.Data.Embeds))
	}
	if len(resp.Data.Components) != 0 {
		t.Errorf("user command: %d component rows, want none", len(resp.Data.Components))
	}

	resp, _ = f.d.userCommandResponse(f.ctx, f.m.Hex(), true)
	got := controls(t, resp)
	for _, a := range []controller.CardAction{controller.ActionSelectVLAN, controller.ActionLogout, controller.ActionChange, controller.ActionCancel} {
		c, ok := got[a]
		if !ok {
			t.Errorf("patch command: no %s control", a)
			continue
		}
		if c.ID.MAC != f.m {
			t.Errorf("patch command: %s carries %s, want %s", a, c.ID.MAC, f.m)
		}
		if b, ok := c.Component.(discordgo.Button); ok && b.Disabled {
			t.Errorf("patch command: %s is disabled", a)
		}
	}

	resp, _ = f.d.userCommandResponse(f.ctx, "nobody-"+f.m.Hex(), true)
	if resp.Data.Content != "No user found with this search string" {
		t.Errorf("unknown user: content %q", resp.Data.Content)
	}
}

func TestComponentResponse_SelectVLAN(t *testing.T) {
	f := newFixture(t, false)
	menuID := controller.CardID{Action: controller.ActionSelectVLAN, MAC: f.m}.String()

	resp, _ := f.d.componentResponse(f.ctx, menuID, []string{"620"})
	if resp.Type != discordgo.InteractionResponseUpdateMessage {
		t.Errorf("type = %v, want an update of the card", resp.Type)
	}
	got := controls(t, resp)
	menu, ok := got[controller.ActionSelectVLAN].Component.(discordgo.SelectMenu)
	if !ok {
		t.Fatal("no VLAN menu")
	}
	for _, o := range menu.Options {
		if want := o.Value == "620"; o.Default != want {
			t.Errorf("option %s Default = %v, want %v", o.Value, o.Default, want)
		}
	}
	if change := got[controller.ActionChange]; change.ID.VLAN != 620 || change.ID.MAC != f.m {
		t.Errorf("change button = %s, want VLAN 620 for %s", change.ID, f.m)
	}
	if b, _ := got[controller.ActionChange].Component.(discordgo.Button); b.Disabled {
		t.Error("change button is disabled")
	}
	if v := f.pending(); v != 0 {
		t.Errorf("selecting a VLAN queued a job to %d", v)
	}

	for _, values := range [][]string{nil, {"x"}} {
		resp, _ = f.d.componentResponse(f.ctx, menuID, values)
		wantMessage(t, resp, msgUnknownInteraction)
	}
}

func TestComponentResponse_Buttons(t *testing.T) {
	for _, tt := range []struct {
		name    string
		with499 bool
		action  controller.CardAction
		vlan    int // VLAN in the change button's ID
		want    int // queued target VLAN, 0 for none
		message string
	}{
		{"change to selected", false, controller.ActionChange, 620, 620, msgVLANChangeQueued},
		{"change without selection", false, controller.ActionChange, 0, 610, msgVLANChangeQueued},
		{"change to unknown VLAN", false, controller.ActionChange, 630, 610, msgVLANChangeQueued},
		{"login", false, controller.ActionLogin, 0, 610, msgVLANChangeQueued},
		{"logout", true, controller.ActionLogout, 0, 499, msgVLANChangeQueued},
		// queueVLANChange maps a VLAN the switch does not have to its primary one, 499 included.
		{"logout on a switch without 499", false, controller.ActionLogout, 0, 610, msgVLANChangeQueued},
		{"cancel", false, controller.ActionCancel, 0, 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.with499)
			id := controller.CardID{Action: tt.action, MAC: f.m, VLAN: tt.vlan}.String()
			resp, _ := f.d.componentResponse(f.ctx, id, nil)
			if resp.Type != discordgo.InteractionResponseUpdateMessage {
				t.Errorf("type = %v, want an update of the card", resp.Type)
			}
			if want := "\n" + tt.message + "\n\nUser found, showing you their card"; resp.Data.Content != want {
				t.Errorf("content = %q, want %q", resp.Data.Content, want)
			}
			if len(resp.Data.Components) != 0 || len(resp.Data.Embeds) != 1 {
				t.Errorf("%d component rows, %d embeds, want the short card without controls",
					len(resp.Data.Components), len(resp.Data.Embeds))
			}
			if got := f.pending(); got != tt.want {
				t.Errorf("queued VLAN = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestComponentResponse_Failures(t *testing.T) {
	f := newFixture(t, false)

	// A card rendered before the IDs carried the MAC, and garbage.
	for _, id := range []string{"findLogoutBtn", "findVlanSelect", "findBogus:" + f.m.Hex()} {
		resp, _ := f.d.componentResponse(f.ctx, id, nil)
		wantMessage(t, resp, msgUnknownInteraction)
	}

	// A client without a session cannot be moved.
	other := f.p.MAC()
	f.p.LoginLog(func(l *model.LoginLogs) { u, h := "eve", other.Hex(); l.Username, l.Mac = &u, &h })
	resp, _ := f.d.componentResponse(f.ctx, controller.CardID{Action: controller.ActionLogin, MAC: other}.String(), nil)
	if !strings.HasPrefix(resp.Data.Content, "\n"+msgVLANChangeFailed+"\n\n") {
		t.Errorf("content = %q, want the failure message", resp.Data.Content)
	}
}
