package discord

import (
	"fmt"
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
	"strings"
)

const (
	CMD_ROOT_NAME  = "net"
	CMD_USER_NAME  = "user"
	CMD_PATCH_NAME = "patch"
)

func (d *Discord) migrateCommands(s *discordgo.Session) error {
	version, err := d.db.GetDiscordVersion()
	if err != nil {
		return err
	}

	commands, err := s.ApplicationCommands(s.State.User.ID, d.guildId)
	if err != nil {
		return err
	}

	tx, err := d.db.BeginTx()
	defer database.Rollback(tx)

	switch version {
	case 0:
		// Initial command registration
		err = d.db.SetDiscordVersion(1, tx)
		if err != nil {
			return err
		}
		dmPermission := false
		var defaultMemberPermission int64 = 0
		var command = &discordgo.ApplicationCommand{
			Name:                     CMD_ROOT_NAME,
			Description:              "PolyLAN network management commands",
			DMPermission:             &dmPermission,
			DefaultMemberPermissions: &defaultMemberPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        CMD_USER_NAME,
					Description: "Finds and edits user network assignments",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "search-string",
							Description: "The string to search for",
							Required:    true,
						},
					},
				},
			},
		}
		cmd, err := s.ApplicationCommandCreate(s.State.User.ID, d.guildId, command)
		if err != nil {
			return err
		}

		d.commandIds[CMD_ROOT_NAME] = cmd.ID
	case 1:
		err = d.db.SetDiscordVersion(2, tx)
		if err != nil {
			return err
		}

		if userCmd, ok := d.commandIds[CMD_ROOT_NAME]; ok {
			err = s.ApplicationCommandDelete(s.State.User.ID, d.guildId, userCmd)
			if err != nil {
				return err
			}
		}

		dmPermission := false
		var defaultMemberPermission int64 = 0
		var command = &discordgo.ApplicationCommand{
			Name:                     CMD_ROOT_NAME,
			Description:              "PolyLAN network management commands",
			DMPermission:             &dmPermission,
			DefaultMemberPermissions: &defaultMemberPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        CMD_USER_NAME,
					Description: "Finds an user by name, email, mac or current ip",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "search-string",
							Description: "The string to search for",
							Required:    true,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        CMD_PATCH_NAME,
					Description: "Finds an user by name, email, mac or current ip and enters a new patch job into the database",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "search-string",
							Description: "The string to search for",
							Required:    true,
						},
					},
				},
			},
		}
		cmd, err := s.ApplicationCommandCreate(s.State.User.ID, d.guildId, command)
		if err != nil {
			return err
		}

		d.commandIds[CMD_ROOT_NAME] = cmd.ID
	}

	if _, ok := d.commandIds[CMD_ROOT_NAME]; !ok {
		for _, cmd := range commands {
			if cmd.Name == CMD_ROOT_NAME {
				d.commandIds[CMD_ROOT_NAME] = cmd.ID
			}
		}
	}

	return tx.Commit()
}

func (d *Discord) handleUserSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, isPatchCommand bool) {
	var user = i.Interaction.Member.User

	if !d.IsAuthorized(user.ID) {
		log.WithFields(log.Fields{
			"id":   user.ID,
			"name": user.Username,
		}).Info("Unauthorized access to command blocked")
		return
	}

	// This is a normal, new find request
	searchString := i.ApplicationCommandData().Options[0].Options[0].StringValue()

	if len(searchString) < 3 {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid user query",
			},
		})

		if err != nil {
			log.WithError(err).Warn("Error during discord reply")
		}
		return
	}

	if strings.Count(searchString, ":") == 5 {
		// Special case mac input
		searchString = strings.Replace(searchString, ":", "", -1)
		searchString = strings.ToLower(searchString)
	}
	if strings.Count(searchString, "-") == 5 {
		// Special case mac input
		searchString = strings.Replace(searchString, "-", "", -1)
		searchString = strings.ToLower(searchString)
	}

	userCard, err := d.dc.GetDiscordUserCard(searchString, true, !isPatchCommand)
	if err != nil {
		err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "User query failed - check logs for more details",
			},
		})

		if err != nil {
			log.WithError(err).Warn("Error during discord reply")
		}
		return
	}
	d.macCache[i.Interaction.ID] = userCard.RealMAC

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    userCard.Content,
			Components: userCard.Components,
			Embeds:     userCard.Embeds,
		},
	})

	if err != nil {
		log.WithError(err).Warn("Error during discord reply")
	}
}

func (d *Discord) handlePatchSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, newOrExisting bool) {
	var user = i.Interaction.Member.User

	if !d.IsAuthorized(user.ID) {
		log.WithFields(log.Fields{
			"id":   user.ID,
			"name": user.Username,
		}).Info("Unauthorized access to command blocked")
		return
	}

	if newOrExisting {
		d.handleUserSubcommand(s, i, true)
	} else {
		// This is an interaction on an existing find request - lets see what we need to do...
		data := i.Interaction.MessageComponentData()
		if data.CustomID == "findVlanSelect" {
			// The selection changed
			d.vlanCache[i.Interaction.Message.ID] = data.Values[0]

			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredMessageUpdate,
			})
			if err != nil {
				log.WithError(err).Warn("Error during discord reply")
			}
		} else if data.CustomID == "findLogoutBtn" || data.CustomID == "findLoginBtn" || data.CustomID == "findChangeBtn" || data.CustomID == "findCancelBtn" {
			var message string
			if data.CustomID != "findCancelBtn" {
				// All these buttons change the VLAN. Lets see...
				var targetVLAN string
				if data.CustomID == "findLogoutBtn" {
					targetVLAN = "499"
				} else {
					var ok bool
					if targetVLAN, ok = d.vlanCache[i.Interaction.Message.ID]; !ok {
						// No vlan specified - fetch the default vlan
						targetVLAN = "1"
					}
				}

				// We need to validate the VLAN choice
				sessions, err := d.db.FindSessionsForMAC(d.macCache[i.Interaction.Message.Interaction.ID])
				if err != nil {
					log.WithError(err).Warn("Error during session lookup")
					message = "***VLAN change failed!***"
				}
				if len(sessions) < 1 {
					log.WithError(err).Warn("Couldn't find an open radius session for the mac specified")
					message = "***VLAN change failed!***"
				}
				switchRef, err := d.db.SwitchByIP(sessions[0].SwitchIP.String())
				if err != nil {
					log.WithError(err).Warn("Error during switch lookup")
					message = "***VLAN change failed!***"
				}
				if switchRef == nil {
					log.WithError(err).Warn("Switch lookup failed")
					message = "***VLAN change failed!***"
				}

				if targetVLAN == "1" {
					targetVLAN = fmt.Sprint(switchRef.PrimaryVlan.VlanID)
				} else {
					ok := false
					for _, vlan := range switchRef.Vlans {
						if fmt.Sprint(vlan.VlanID) == targetVLAN {
							ok = true
						}
					}
					if !ok {
						targetVLAN = fmt.Sprint(switchRef.PrimaryVlan.VlanID)
					}
				}

				// We need to get the id of the original message that triggered the original interaction
				err = d.dc.ChangeUserVLAN(d.macCache[i.Interaction.Message.Interaction.ID], targetVLAN)
				message = "**VLAN change queued**"
				if err != nil {
					log.WithError(err).Warn("Error during vlan change request")
					message = "***VLAN change failed!***"
				}
			} else {
				message = ""
			}

			// Also, lets update the user card so that stuff is disabled...
			card, err := d.dc.GetDiscordUserCard(d.macCache[i.Interaction.Message.Interaction.ID], false, false)

			if err != nil {
				log.WithError(err).Warn("Error during discord card update")
			} else {
				err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: &discordgo.InteractionResponseData{
						Content:    "\n" + message + "\n\n" + card.Content,
						Components: card.Components,
						Embeds:     card.Embeds,
					},
				})
				if err != nil {
					log.WithError(err).Warn("Error during discord card update")
				}
			}
		} else {
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Unknown interaction request - this could be a bug",
				},
			})
			if err != nil {
				log.WithError(err).Warn("Error during discord reply")
			}
		}
	}
}

func (d *Discord) setupHandlers(s *discordgo.Session) error {
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type == discordgo.InteractionApplicationCommand {
			if i.ApplicationCommandData().Name == CMD_ROOT_NAME && i.ApplicationCommandData().Options[0].Name == CMD_USER_NAME {
				// We're supposed to find someone!
				d.handleUserSubcommand(s, i, false)
			} else if i.ApplicationCommandData().Name == CMD_ROOT_NAME && i.ApplicationCommandData().Options[0].Name == CMD_PATCH_NAME {
				// We're supposed to patch someone!
				d.handlePatchSubcommand(s, i, true)
			}
		} else if i.Type == discordgo.InteractionMessageComponent {
			if strings.Index(i.Interaction.MessageComponentData().CustomID, "find") == 0 {
				d.handlePatchSubcommand(s, i, false)
			}
		}
	})

	return nil
}
