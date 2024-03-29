package controller

import (
	"bytes"
	"database/sql"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"

	"github.com/VSETH-GECO/bouncer/pkg/database"
)

type DiscordController struct {
	db *database.Handler
}

type UserCard struct {
	Content    string
	Components []discordgo.MessageComponent
	Embeds     []*discordgo.MessageEmbed
	RealMAC    string
}

func NewDiscordController(db *database.Handler) *DiscordController {
	return &DiscordController{
		db: db,
	}
}

func convertDate(time sql.NullTime) string {
	if !time.Valid {
		return "null"
	}

	date, err := time.Value()
	if err != nil {
		return "N/A"
	}

	return fmt.Sprint(date)
}

func convertData(data int) string {
	return fmt.Sprint(data)
}

func (dc *DiscordController) GetDiscordUserCard(searchString string, buttonsEnabled bool, shortCard bool) (*UserCard, error) {
	card := &UserCard{
		Content: "",
	}

	mac, err := dc.db.FindMAC(searchString)
	if err != nil {
		log.WithError(err).Warn("User MAC lookup failed!")
		return nil, err
	}

	if mac == "" {
		card.Content = "No user found with this search string"
		return card, nil
	}

	card.RealMAC = mac

	user, err := dc.db.LoadUser(mac)
	if err != nil {
		log.WithError(err).Warn("Load user failed!")
		return nil, err
	}
	if user == nil {
		card.Content = "No user found with this search string"
		return card, nil
	}

	var online string
	var loggedIn string
	if len(user.Sessions) > 0 && !user.Sessions[0].EndDate.Valid {
		online = "✅"
	} else {
		online = "❌"
	}

	loginVlan, err := dc.db.CheckUserSignedInNoTx(user.Mac)
	if err != nil {
		log.WithError(err).Warn("Check user signed in failed!")
		return nil, err
	}
	if loginVlan > 0 {
		loggedIn = "✅"
	} else {
		loggedIn = "❌"
	}

	if user.Hostname == "" {
		user.Hostname = "_unknown_"
	}
	if user.Name == "" {
		user.Name = "_unknown_"
	}

	var extraFields []*discordgo.MessageEmbedField
	if len(user.Sessions) > 0 {
		if !user.Sessions[0].EndDate.Valid {
			online = online + " since " + convertDate(user.Sessions[0].StartDate)
		} else {
			online = online + " since " + convertDate(user.Sessions[0].EndDate)
		}

		if switchObj, err := dc.db.SwitchByIP(user.Sessions[0].SwitchIP.String()); err == nil {
			extraFields = []*discordgo.MessageEmbedField{
				{
					Name:  "Switch name",
					Value: switchObj.Hostname,
				},
				{
					Name:  "Switch location",
					Value: switchObj.Location,
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
					Value: user.Mac,
				},
				{
					Name:   "Hostname",
					Value:  user.Hostname,
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
					Value:  user.Name,
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

	if !shortCard && len(user.Sessions) > 0 {
		// We have RADIUS data, so lets put that in here
		buf := bytes.Buffer{}
		table := tablewriter.NewWriter(&buf)
		table.SetHeader([]string{
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
		for _, session := range user.Sessions {
			count++
			if count >= 3 {
				break
			}
			var switchName string
			mySwitch, err := dc.db.SwitchByIP(session.SwitchIP.String())
			if err != nil {
				log.WithError(err).Warn("Switch lookup failed")
				switchName = "<unknown>"
			} else {
				if mySwitch != nil {
					switchName = mySwitch.Hostname + " (" + mySwitch.Location + ")"
				} else {
					switchName = "<unknown>"
				}
			}

			row := []string{
				convertDate(session.StartDate),
				convertDate(session.EndDate),
				session.EndReason,
				switchName,
				session.SwitchPort,
				session.ClientIP.String(),
				convertData(session.BytesReceived),
				convertData(session.BytesSent),
			}
			table.Append(row)
		}
		table.Render()

		card.Content = "Sessions in descending order:\n```md\n" + buf.String() + "\n```"
	} else if shortCard {
		card.Content = "User found, showing you their card"
	} else {
		card.Content = "No session found for this user, check back once they're plugged in."
	}

	if !shortCard {
		// Available VLANs
		var vlanRow discordgo.ActionsRow
		vlansMissing := false
		if len(user.Sessions) > 0 {
			switchIP := user.Sessions[0].SwitchIP
			switchRef, err := dc.db.SwitchByIP(switchIP.String())
			if err != nil {
				log.WithError(err).Warn("Switch lookup failed")
			}
			if switchRef == nil {
				log.WithField("ip", switchIP.String()).Warn("Unknown switch returned from RADIUS query!")
			}
			if err != nil || switchRef == nil {
				log.Debug("vlan is missing")
				vlansMissing = true
			} else {
				log.Debug("crafting vlan menu")
				var vlanMenu []discordgo.SelectMenuOption
				for _, vlan := range switchRef.Vlans {
					isDefault := switchRef.PrimaryVlan == vlan
					vlanMenu = append(vlanMenu, discordgo.SelectMenuOption{
						Description: vlan.Description,
						Value:       fmt.Sprint(vlan.VlanID),
						Label:       fmt.Sprintf("VLAN %d (%s) - %s", vlan.VlanID, vlan.Name, vlan.IpRange.String()),
						Default:     isDefault,
					})
				}
				vlanRow = discordgo.ActionsRow{
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
		} else {
			log.Debug("vlan is missing")
			vlansMissing = true
		}

		// Actions!
		var actions []discordgo.MessageComponent
		if loginVlan > 0 {
			// User is logged in ... so we can log them out or change their VLAN
			log.Debug("adding 'logout button' to action menu")
			actions = append(actions, discordgo.Button{
				Label:    "Logout",
				Style:    discordgo.DangerButton,
				Disabled: !buttonsEnabled,
				Emoji: discordgo.ComponentEmoji{
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
					Emoji: discordgo.ComponentEmoji{
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
				Emoji: discordgo.ComponentEmoji{
					Name: "✔️",
				},
				CustomID: "findLoginBtn",
			})
		}

		if !vlansMissing {
			card.Components = append(card.Components, vlanRow)
			log.Debug("adding 'cancel' button to action menu")
			actions = append(actions, discordgo.Button{
				Label:    "Cancel",
				Style:    discordgo.SecondaryButton,
				Disabled: !buttonsEnabled,
				Emoji: discordgo.ComponentEmoji{
					Name: "✖️",
				},
				CustomID: "findCancelBtn",
			})
			card.Components = append(card.Components, discordgo.ActionsRow{
				Components: actions,
			})
		}
	}

	return card, nil
}

func (dc *DiscordController) ChangeUserVLAN(mac string, targetVLAN string) error {
	return dc.db.CreateNewJob(mac, targetVLAN)
}
