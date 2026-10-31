package controller

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/VSETH-GECO/bouncer/internal/mac"
)

// CardAction is what a card control does.
type CardAction string

const (
	ActionSelectVLAN CardAction = "findVlanSelect"
	ActionLogout     CardAction = "findLogoutBtn"
	ActionLogin      CardAction = "findLoginBtn"
	ActionChange     CardAction = "findChangeBtn"
	ActionCancel     CardAction = "findCancelBtn"
)

// valid reports whether a is one of the known card actions.
func (a CardAction) valid() bool {
	switch a {
	case ActionSelectVLAN, ActionLogout, ActionLogin, ActionChange, ActionCancel:
		return true
	}
	return false
}

// carriesVLAN reports whether a control of action a carries the VLAN chosen in the card's menu: the change and the
// login button do.
func (a CardAction) carriesVLAN() bool {
	return a == ActionChange || a == ActionLogin
}

// maxVLAN is the highest valid 802.1Q VLAN ID.
const maxVLAN = 4094

// CardID identifies a control on a user card together with the state the control needs: the client's MAC and, for
// the change and the login button, the VLAN chosen in the menu (0 when none was chosen). It is encoded into the Discord CustomID
// so the bot keeps no state between interactions.
type CardID struct {
	Action CardAction
	MAC    mac.MAC
	VLAN   int
}

// String encodes c as a Discord CustomID: "<action>:<mac hex>", or "<action>:<mac hex>:<vlan>" for ActionChange and
// ActionLogin. The VLAN of any other action is not encoded.
func (c CardID) String() string {
	if c.Action.carriesVLAN() {
		return fmt.Sprintf("%s:%s:%d", c.Action, c.MAC.Hex(), c.VLAN)
	}
	return fmt.Sprintf("%s:%s", c.Action, c.MAC.Hex())
}

// ParseCardID decodes a CustomID produced by CardID.String. An unknown action, a missing or malformed MAC, or a
// missing or malformed VLAN on ActionChange or ActionLogin is an error; so is a bare action from a card rendered before the IDs
// carried the MAC.
func ParseCardID(s string) (CardID, error) {
	parts := strings.Split(s, ":")
	id := CardID{Action: CardAction(parts[0])}
	if !id.Action.valid() {
		return CardID{}, fmt.Errorf("card id %q: unknown action %q", s, parts[0])
	}
	want := 2
	if id.Action.carriesVLAN() {
		want = 3
	}
	if len(parts) != want {
		return CardID{}, fmt.Errorf("card id %q: want %d fields, got %d", s, want, len(parts))
	}
	m, err := mac.Parse(parts[1])
	if err != nil || len(parts[1]) != 12 {
		return CardID{}, fmt.Errorf("card id %q: bad MAC %q", s, parts[1])
	}
	id.MAC = m
	if id.Action.carriesVLAN() {
		v, err := strconv.Atoi(parts[2])
		if err != nil || v < 0 || v > maxVLAN || strconv.Itoa(v) != parts[2] {
			return CardID{}, fmt.Errorf("card id %q: bad VLAN %q", s, parts[2])
		}
		id.VLAN = v
	}
	return id, nil
}
