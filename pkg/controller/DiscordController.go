package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
)

type DiscordController struct {
	db *db.DB
}

type UserCard struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
	RealMAC    string
}

func NewDiscordController(d *db.DB) *DiscordController {
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

func (dc *DiscordController) GetDiscordUserCard(ctx context.Context, searchString string, buttonsEnabled bool, longCard bool) (*UserCard, error) {
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
			log.WithError(err).Warn("Switch lookup failed")
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

	if longCard && len(user.Sessions) > 0 {
		// We have RADIUS data, so lets put that in here
		buf := bytes.Buffer{}
		dc.renderLastSessions(ctx, &buf, user.Sessions)
		card.Content = "Sessions in descending order:\n```md\n" + buf.String() + "\n```"
	} else if !longCard {
		card.Content = "User found, showing you their card"
	} else {
		card.Content = "No session found for this user, check back once they're plugged in."
	}

	if longCard {
		dc.assembleActions(ctx, card, user, vlan, buttonsEnabled)
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
			log.WithError(err).Warn("Switch lookup failed")
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

func (dc *DiscordController) assembleActions(ctx context.Context, card *UserCard, user *User, vlan int, buttonsEnabled bool) {
	// Available VLANs
	availableVLANs := dc.collectAvailableVLANs(ctx, user, buttonsEnabled)
	vlansMissing := availableVLANs == nil
	if vlansMissing {
		log.Debug("no vlan available")
	}

	// Actions!
	var actions []discordgo.MessageComponent
	if vlan > 0 && vlan != 499 {
		// User is logged in ... so we can log them out or change their VLAN
		log.Debug("adding 'logout button' to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Logout",
			Style:    discordgo.DangerButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✖️",
			},
			CustomID: "findLogoutBtn",
		})
		if !vlansMissing {
			log.Debug("adding 'change vlan' button to action menu")
			actions = append(actions, discordgo.Button{
				Label:    "Change VLAN",
				Style:    discordgo.PrimaryButton,
				Disabled: !buttonsEnabled,
				Emoji: &discordgo.ComponentEmoji{
					Name: "🔧",
				},
				CustomID: "findChangeBtn",
			})
		}
	} else if len(user.Sessions) > 0 && !vlansMissing {
		// User is not even logged in, but we at least have a session
		log.Debug("adding 'login' button to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Login",
			Style:    discordgo.SuccessButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✔️",
			},
			CustomID: "findLoginBtn",
		})
	}

	if !vlansMissing {
		card.Components = append(card.Components, availableVLANs)
		log.Debug("adding 'cancel' button to action menu")
		actions = append(actions, discordgo.Button{
			Label:    "Cancel",
			Style:    discordgo.SecondaryButton,
			Disabled: !buttonsEnabled,
			Emoji: &discordgo.ComponentEmoji{
				Name: "✖️",
			},
			CustomID: "findCancelBtn",
		})
		card.Components = append(card.Components, discordgo.ActionsRow{
			Components: actions,
		})
	}
}

func (dc *DiscordController) collectAvailableVLANs(ctx context.Context, user *User, buttonsEnabled bool) *discordgo.ActionsRow {
	if len(user.Sessions) == 0 {
		return nil
	}

	switchIP := user.Sessions[0].SwitchIP
	switchRef, err := switches.ByIP(ctx, dc.db.Querier(), switchIP)
	if errors.Is(err, db.ErrNotFound) {
		log.WithField("ip", switchIP.String()).Warn("Unknown switch returned from RADIUS query!")
		return nil
	}
	if err != nil {
		log.WithError(err).Warn("Switch lookup failed")
		return nil
	}

	log.Debug("crafting vlan menu")
	var vlanMenu []discordgo.SelectMenuOption
	for _, vlan := range switchRef.VLANs {
		isDefault := switchRef.PrimaryVLAN != nil && switchRef.PrimaryVLAN.VLANID == vlan.VLANID
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
				CustomID:    "findVlanSelect",
				Placeholder: "Default VLAN",
				Options:     vlanMenu,
				Disabled:    !buttonsEnabled,
			},
		},
	}
}

func (dc *DiscordController) ChangeUserVLAN(ctx context.Context, m mac.MAC, vlan int) error {
	return jobs.Create(ctx, dc.db.Querier(), m, vlan)
}
