package controller

import (
	"strings"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/mac"
)

func TestCardID_RoundTrip(t *testing.T) {
	m := mac.MustParse("aa:bb:cc:dd:ee:01")
	ids := []CardID{
		{Action: ActionSelectVLAN, MAC: m},
		{Action: ActionLogout, MAC: m},
		{Action: ActionLogin, MAC: m},
		{Action: ActionCancel, MAC: m},
		{Action: ActionChange, MAC: m},
		{Action: ActionChange, MAC: m, VLAN: 20},
		{Action: ActionChange, MAC: m, VLAN: 4094},
	}
	for _, id := range ids {
		s := id.String()
		if len(s) > 100 {
			t.Errorf("%q is %d characters, Discord allows 100", s, len(s))
		}
		if !strings.HasPrefix(s, string(id.Action)+":") {
			t.Errorf("%q does not start with the action %q", s, id.Action)
		}
		got, err := ParseCardID(s)
		if err != nil {
			t.Errorf("ParseCardID(%q): %v", s, err)
			continue
		}
		if got != id {
			t.Errorf("ParseCardID(%q) = %+v, want %+v", s, got, id)
		}
	}
}

func TestCardID_Encoding(t *testing.T) {
	m := mac.MustParse("aa:bb:cc:dd:ee:01")
	for _, tt := range []struct {
		id   CardID
		want string
	}{
		{CardID{ActionLogout, m, 0}, "findLogoutBtn:aabbccddee01"},
		{CardID{ActionLogout, m, 30}, "findLogoutBtn:aabbccddee01"}, // only the change button carries a VLAN
		{CardID{ActionChange, m, 0}, "findChangeBtn:aabbccddee01:0"},
		{CardID{ActionChange, m, 20}, "findChangeBtn:aabbccddee01:20"},
		{CardID{ActionSelectVLAN, m, 0}, "findVlanSelect:aabbccddee01"},
	} {
		if got := tt.id.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestParseCardID_Rejects(t *testing.T) {
	for _, s := range []string{
		"",
		"findLogoutBtn", // a card rendered by an older build
		"findChangeBtn",
		"findVlanSelect",
		"findBogusBtn:aabbccddee01",
		"findLogoutBtn:",
		"findLogoutBtn:aabbccddee0",
		"findLogoutBtn:zzbbccddee01",
		"findLogoutBtn:aa:bb:cc:dd:ee:01",
		"findLogoutBtn:aabbccddee01:20",
		"findChangeBtn:aabbccddee01",
		"findChangeBtn:aabbccddee01:",
		"findChangeBtn:aabbccddee01:x",
		"findChangeBtn:aabbccddee01:-1",
		"findChangeBtn:aabbccddee01:4095",
		"findChangeBtn:aabbccddee01:+20",
		"findChangeBtn:aabbccddee01:20:1",
	} {
		if id, err := ParseCardID(s); err == nil {
			t.Errorf("ParseCardID(%q) = %+v, want an error", s, id)
		}
	}
}
