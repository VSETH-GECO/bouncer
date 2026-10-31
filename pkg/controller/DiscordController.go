package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/olekukonko/tablewriter"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
)

type DiscordController struct {
	db Store
}

type UserCard struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
	RealMAC    string
}

func NewDiscordController(d Store) *DiscordController {
	return &DiscordController{
		db: d,
	}
}

func convertDate(t *time.Time) string {
	if t == nil {
		return "null"
	}
	return fmt.Sprint(*t)
}

func convertData(data *int64) string {
	if data == nil {
		return "null"
	}
	return fmt.Sprint(*data)
}

// CardOptions selects how GetDiscordUserCard renders a card.
type CardOptions struct {
	// Buttons enables the card's controls; a card without it shows them disabled.
	Buttons bool
	// Long adds the recent sessions and the controls (VLAN menu and buttons) to the card.
	Long bool
	// SelectedVLAN is the VLAN chosen in the card's menu, 0 for none. The menu shows it as chosen when it is one of
	// the switch's VLANs (else the switch's primary VLAN), and the change and login buttons carry it.
	SelectedVLAN int
}

// GetDiscordUserCard looks up the client matching searchString and renders its card. A search that matches nothing
// yields a card whose Content says so.
func (dc *DiscordController) GetDiscordUserCard(ctx context.Context, searchString string, opts CardOptions) (*UserCard, error) {
	card := &UserCard{
		Content: "",
	}

	q := dc.db.Querier()
	m, err := findMAC(ctx, q, searchString)
	if errors.Is(err, db.ErrNotFound) {
		card.Content = "No user found with this search string"
		return card, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up user MAC: %w", err)
	}

	card.RealMAC = m.Hex()

	user, err := loadUser(ctx, q, m)
	if err != nil {
		return nil, fmt.Errorf("loading user %s: %w", m, err)
	}

	var online string
	var loggedIn string
	if len(user.Sessions) > 0 && user.Sessions[0].Stop == nil {
		online = "✅"
	} else {
		online = "❌"
	}

	vlan, err := freeradius.AssignedVLAN(ctx, q, user.MAC)
	if errors.Is(err, db.ErrNotFound) {
		vlan = -1
	} else if err != nil {
		return nil, fmt.Errorf("checking whether user %s is signed in: %w", user.MAC, err)
	}
	if vlan > 0 && vlan != 499 {
		loggedIn = "✅"
	} else {
		loggedIn = "❌"
	}

	hostname := derefOr(user.Hostname, "_unknown_")
	name := derefOr(&user.Name, "_unknown_")

	var extraFields []*discordgo.MessageEmbedField
	if len(user.Sessions) > 0 {
		if user.Sessions[0].Stop == nil {
			online = online + " since " + convertDate(user.Sessions[0].Start)
		} else {
			online = online + " since " + convertDate(user.Sessions[0].Stop)
		}

		switchObj, err := switches.ByIP(ctx, dc.db.Querier(), user.Sessions[0].SwitchIP)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			slog.Warn("Switch lookup failed", "error", err)
		}
		if err == nil {
			extraFields = []*discordgo.MessageEmbedField{
				{
					Name:  "Switch name",
					Value: switchObj.Hostname,
				},
				{
					Name:  "Switch port",
					Value: derefOr(user.Sessions[0].SwitchPort, "_unknown_"),
				},
				{
					Name:  "Switch location",
					Value: derefOr(switchObj.Location, "_unknown_"),
				},
			}
		}
	}

	card.Embeds = []*discordgo.MessageEmbed{
		{
			Title: "User info",
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:  "MAC",
					Value: user.MAC.String(),
				},
				{
					Name:   "Hostname",
					Value:  hostname,
					Inline: true,
				},
				{
					Name:   "IP",
					Value:  user.IP.String(),
					Inline: true,
				},
				{
					Name:   "IPv6",
					Value:  user.IP6.String(),
					Inline: true,
				},
				{
					Name:   "Username",
					Value:  name,
					Inline: true,
				},
				{
					Name:   "Email",
					Value:  derefOr(&user.Email, "_unknown_"),
					Inline: true,
				},
				{
					Name:  "Online",
					Value: online,
				},
				{
					Name:  "Logged in",
					Value: loggedIn,
				},
			},
		},
	}

	if extraFields != nil {
		card.Embeds[0].Fields = append(card.Embeds[0].Fields, extraFields...)
	}

	if opts.Long && len(user.Sessions) > 0 {
		// We have RADIUS data, so lets put that in here
		buf := bytes.Buffer{}
		dc.renderLastSessions(ctx, &buf, user.Sessions)
		card.Content = "Sessions in descending order:\n```md\n" + buf.String() + "\n```"
	} else if !opts.Long {
		card.Content = "User found, showing you their card"
	} else {
		card.Content = "No session found for this user, check back once they're plugged in."
	}

	if opts.Long {
		dc.assembleActions(ctx, card, user, vlan, opts)
	}

	return card, nil
}

func (dc *DiscordController) renderLastSessions(ctx context.Context, buf *bytes.Buffer, sessions []freeradius.Session) {
	table := tablewriter.NewWriter(buf)
	table.Header([]string{
		"Start",
		"End",
		"End reason",
		"Switch name (+loc)",
		"Switch port",
		"Assigned IP",
		"Received",
		"Sent",
	})
	count := 0
	for _, session := range sessions {
		count++
		if count >= 3 {
			break
		}
		switchName := "<unknown>"
		mySwitch, err := switches.ByIP(ctx, dc.db.Querier(), session.SwitchIP)
		switch {
		case err == nil:
			switchName = mySwitch.Hostname + " (" + derefOr(mySwitch.Location, "") + ")"
		case !errors.Is(err, db.ErrNotFound):
			slog.Warn("Switch lookup failed", "error", err)
		}

		row := []string{
			convertDate(session.Start),
			convertDate(session.Stop),
			session.TerminateCause,
			switchName,
			derefOr(session.SwitchPort, ""),
			session.ClientIP.String(),
			convertData(session.BytesIn),
			convertData(session.BytesOut),
		}
		table.Append(row)
	}
	table.Render()
}

func (dc *DiscordController) assembleActions(ctx context.Context, card *UserCard, user *User, vlan int, opts CardOptions) {
	buttonsEnabled := opts.Buttons
	id := func(a CardAction) string { return CardID{Action: a, MAC: user.MAC}.String() }
	// Available VLANs
	availableVLANs := dc.collectAvailableVLANs(ctx, user, opts)
	vlansMissing := availableVLANs == nil
	if vlansMissing {
		slog.Debug("no vlan available")
	}

	// Actions!
	var actions []discordgo.MessageComponent
	if vlan > 0 && vlan != 499 {
		// User is logged in ... so we can log them out or change their VLAN
		slog.Debug("adding 'logout button' to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Logout",
			Style:    discordgo.DangerButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✖️",
			},
			CustomID: id(ActionLogout),
		})
		if !vlansMissing {
			slog.Debug("adding 'change vlan' button to action menu")
			actions = append(actions, discordgo.Button{
				Label:    "Change VLAN",
				Style:    discordgo.PrimaryButton,
				Disabled: !buttonsEnabled,
				Emoji: &discordgo.ComponentEmoji{
					Name: "🔧",
				},
				CustomID: CardID{Action: ActionChange, MAC: user.MAC, VLAN: opts.SelectedVLAN}.String(),
			})
		}
	} else if len(user.Sessions) > 0 && !vlansMissing {
		// User is not even logged in, but we at least have a session
		slog.Debug("adding 'login' button to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Login",
			Style:    discordgo.SuccessButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✔️",
			},
			CustomID: CardID{Action: ActionLogin, MAC: user.MAC, VLAN: opts.SelectedVLAN}.String(),
		})
	}

	if !vlansMissing {
		card.Components = append(card.Components, availableVLANs)
		slog.Debug("adding 'cancel' button to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Cancel",
			Style:    discordgo.SecondaryButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✖️",
			},
			CustomID: id(ActionCancel),
		})
		card.Components = append(card.Components, discordgo.ActionsRow{
			Components: actions,
		})
	}
}

// collectAvailableVLANs renders the VLAN menu of the switch the client's newest session is on, or returns nil if that
// switch is unknown. The option for opts.SelectedVLAN is the default when the switch has it, else the primary VLAN's.
func (dc *DiscordController) collectAvailableVLANs(ctx context.Context, user *User, opts CardOptions) *discordgo.ActionsRow {
	if len(user.Sessions) == 0 {
		return nil
	}

	switchIP := user.Sessions[0].SwitchIP
	switchRef, err := switches.ByIP(ctx, dc.db.Querier(), switchIP)
	if errors.Is(err, db.ErrNotFound) {
		slog.Warn("Unknown switch returned from RADIUS query!", "ip", switchIP.String())
		return nil
	}
	if err != nil {
		slog.Warn("Switch lookup failed", "error", err)
		return nil
	}

	slog.Debug("crafting vlan menu")
	chosen := -1
	if switchRef.PrimaryVLAN != nil {
		chosen = switchRef.PrimaryVLAN.VLANID
	}
	for _, vlan := range switchRef.VLANs {
		if opts.SelectedVLAN != 0 && vlan.VLANID == opts.SelectedVLAN {
			chosen = opts.SelectedVLAN
		}
	}
	var vlanMenu []discordgo.SelectMenuOption
	for _, vlan := range switchRef.VLANs {
		isDefault := vlan.VLANID == chosen
		vlanMenu = append(vlanMenu, discordgo.SelectMenuOption{
			Description: vlan.Description,
			Value:       fmt.Sprint(vlan.VLANID),
			Label:       fmt.Sprintf("VLAN %d (%s) - %s", vlan.VLANID, vlan.Name, vlan.IPRange.String()),
			Default:     isDefault,
		})
	}
	return &discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{
				MaxValues:   1,
				CustomID:    CardID{Action: ActionSelectVLAN, MAC: user.MAC}.String(),
				Placeholder: "Default VLAN",
				Options:     vlanMenu,
				Disabled:    !opts.Buttons,
			},
		},
	}
}

func (dc *DiscordController) ChangeUserVLAN(ctx context.Context, m mac.MAC, vlan int) error {
	return jobs.Create(ctx, dc.db.Querier(), m, vlan)
}
