package discord

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
)

// newLogoutFixture is newFixture for tests that insert a logout, see testdb.LockLogouts.
func newLogoutFixture(t *testing.T, with499 bool) *fixture {
	t.Helper()
	testdb.LockLogouts(t)
	return newFixture(t, with499)
}

func logoutTime() string {
	return time.Now().UTC().Add(2 * time.Hour).Format("2006-01-02 15:04")
}

func wantContent(t *testing.T, resp *discordgo.InteractionResponse, texts ...string) {
	t.Helper()
	if resp.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Errorf("response type = %v, want a new message", resp.Type)
	}
	for _, text := range texts {
		if !strings.Contains(resp.Data.Content, text) {
			t.Errorf("content = %q, want it to contain %q", resp.Data.Content, text)
		}
	}
}

func TestLogoutCommand_ScheduleThenArm(t *testing.T) {
	f := newLogoutFixture(t, false)
	at := logoutTime()

	resp, _ := f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	wantContent(t, resp, "proposed", at+" UTC", "<@111>", "second admin")

	resp, _ = f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	wantContent(t, resp, "second admin")

	resp, _ = f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_SCHEDULE_NAME, at)
	wantContent(t, resp, "armed", at+" UTC", "<@111>", "<@222>")

	l, err := logout.InFlight(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	if l.State != logout.StateArmed {
		t.Errorf("state = %s, want armed", l.State)
	}
}

func TestLogoutCommand_BadTimes(t *testing.T) {
	f := newLogoutFixture(t, false)
	resp, _ := f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, "whenever")
	wantContent(t, resp, "Can't read")

	soon := time.Now().UTC().Add(time.Minute).Format("2006-01-02 15:04")
	resp, _ = f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, soon)
	wantContent(t, resp, "at least 1 hour")

	if _, err := logout.InFlight(f.ctx, f.tx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("a refused command left a logout in flight: %v", err)
	}
}

func TestLogoutCommand_StatusAndCancel(t *testing.T) {
	f := newLogoutFixture(t, false)
	resp, _ := f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_STATUS_NAME, "")
	wantContent(t, resp, "No global logout")
	resp, _ = f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_CANCEL_NAME, "")
	wantContent(t, resp, "No global logout")

	at := logoutTime()
	f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	resp, _ = f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_STATUS_NAME, "")
	wantContent(t, resp, "proposed", at+" UTC", "<@111>", "1 client")

	resp, _ = f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_CANCEL_NAME, "")
	wantContent(t, resp, "cancelled", "<@222>")
	if _, err := logout.InFlight(f.ctx, f.tx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("after cancel: %v, want nothing in flight", err)
	}
}

func TestLogoutCommand_CancelWhileRunningSaysWhatStays(t *testing.T) {
	f := newLogoutFixture(t, false)
	at := logoutTime()
	f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_SCHEDULE_NAME, at)
	l, err := logout.InFlight(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if ok, err := logout.Start(f.ctx, f.tx, l.ID, now, now.Add(15*time.Minute)); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}

	resp, _ := f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_CANCEL_NAME, "")
	wantContent(t, resp, "cancelled", "already moved")
}

func TestLogoutCancelButton(t *testing.T) {
	f := newLogoutFixture(t, false)
	at := logoutTime()
	f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_SCHEDULE_NAME, at)
	l, err := logout.InFlight(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}

	// A button of another logout and garbage cancel nothing.
	resp, _ := f.d.logoutCancelButtonResponse(f.ctx, "111", logoutCancelID(l.ID+1))
	wantContent(t, resp, "already finished")
	for _, id := range []string{"logoutCancel:", "logoutCancel:x", "logoutCancel:0", "logoutOther:1"} {
		resp, _ = f.d.logoutCancelButtonResponse(f.ctx, "111", id)
		wantMessage(t, resp, msgUnknownInteraction)
	}
	if got, err := logout.InFlight(f.ctx, f.tx); err != nil || got.State != logout.StateArmed {
		t.Fatalf("in flight = %+v, %v, want it still armed", got, err)
	}

	resp, _ = f.d.logoutCancelButtonResponse(f.ctx, "111", logoutCancelID(l.ID))
	wantContent(t, resp, "cancelled", "<@111>")
	resp, _ = f.d.logoutCancelButtonResponse(f.ctx, "111", logoutCancelID(l.ID))
	wantContent(t, resp, "already finished")
}

func TestNotifyWithoutSession(t *testing.T) {
	f := newFixture(t, false)
	if err := f.d.Notify(f.ctx, "hello"); !errors.Is(err, errNoSession) {
		t.Errorf("Notify = %v, want errNoSession", err)
	}
}

func TestWarningMessage(t *testing.T) {
	at := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	msg := warningMessage(controller.LogoutWarning{
		Logout:    logout.Logout{ID: 7, ScheduledFor: at},
		Stage:     5,
		Approvers: []string{"111", "222"},
		Clients:   42,
	}, []string{"111", "222", "333"})

	for _, want := range []string{"<@333>", "2026-11-01 15:00 UTC", "42 client(s)", "VLAN 499", "15 minutes", "approved by <@111>, <@222>"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("content = %q, want it to contain %q", msg.Content, want)
		}
	}
	if len(msg.Components) != 1 {
		t.Fatalf("component rows = %d, want 1", len(msg.Components))
	}
	row, ok := msg.Components[0].(discordgo.ActionsRow)
	if !ok || len(row.Components) != 1 {
		t.Fatalf("row = %#v, want one button", msg.Components[0])
	}
	button, ok := row.Components[0].(discordgo.Button)
	if !ok || button.CustomID != logoutCancelID(7) {
		t.Errorf("button = %#v, want CustomID %s", row.Components[0], logoutCancelID(7))
	}
}

func TestWarnWithoutSession(t *testing.T) {
	f := newFixture(t, false)
	if err := f.d.Warn(f.ctx, controller.LogoutWarning{}); !errors.Is(err, errNoSession) {
		t.Errorf("Warn = %v, want errNoSession", err)
	}
}

func TestCardButtons_DuringALockout(t *testing.T) {
	f := newLogoutFixture(t, false)
	at := logoutTime()
	f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_SCHEDULE_NAME, at)
	l, err := logout.InFlight(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	ends := now.Add(15 * time.Minute)
	if ok, err := logout.Start(f.ctx, f.tx, l.ID, now, ends); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}

	id := controller.CardID{Action: controller.ActionChange, MAC: f.m, VLAN: 620}.String()
	resp, _ := f.d.componentResponse(f.ctx, id, nil)
	want := fmt.Sprintf(msgVLANChangeIgnored, ends.Format("15:04"))
	if !strings.Contains(resp.Data.Content, want) {
		t.Errorf("content = %q, want it to contain %q", resp.Data.Content, want)
	}
	if got := f.pending(); got != 0 {
		t.Errorf("queued VLAN = %d, want nothing queued", got)
	}
}

func TestCardButtons_LogoutWorksDuringALockout(t *testing.T) {
	f := newLogoutFixture(t, true)
	at := logoutTime()
	f.d.logoutCommandResponse(f.ctx, "111", CMD_LOGOUT_SCHEDULE_NAME, at)
	f.d.logoutCommandResponse(f.ctx, "222", CMD_LOGOUT_SCHEDULE_NAME, at)
	l, err := logout.InFlight(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if ok, err := logout.Start(f.ctx, f.tx, l.ID, now, now.Add(15*time.Minute)); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}

	id := controller.CardID{Action: controller.ActionLogout, MAC: f.m}.String()
	resp, _ := f.d.componentResponse(f.ctx, id, nil)
	if !strings.Contains(resp.Data.Content, msgVLANChangeQueued) {
		t.Errorf("content = %q, want it queued", resp.Data.Content)
	}
	if got := f.pending(); got != freeradius.LoginVLAN {
		t.Errorf("queued VLAN = %d, want the login VLAN", got)
	}
}
