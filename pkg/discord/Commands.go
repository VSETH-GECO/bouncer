package discord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"

	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/database"
)

const (
	CMD_ROOT_NAME  = "net"
	CMD_USER_NAME  = "user"
	CMD_PATCH_NAME = "patch"
)

func (d *Discord) migrateCommands(ctx context.Context, s *discordgo.Session) error {
	version, err := d.db.GetDiscordVersion(ctx)
	if errors.Is(err, database.ErrNotFound) {
		log.Warn("No previous discord migration entry found, assuming a fresh start")
		version = 0
	} else if err != nil {
		return err
	}

	commands, err := s.ApplicationCommands(s.State.User.ID, d.guildId)
	if err != nil {
		return fmt.Errorf("listing application commands: %w", err)
	}

	return d.db.WithTx(ctx, func(tx *sql.Tx) error {
		return d.migrateCommandsTx(ctx, tx, s, version, commands)
	})
}

func (d *Discord) migrateCommandsTx(ctx context.Context, tx *sql.Tx, s *discordgo.Session, version int, commands []*discordgo.ApplicationCommand) error {
	switch version {
	case 0:
		// Initial command registration
		err := d.db.SetDiscordVersion(ctx, tx, 1)
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
			return fmt.Errorf("creating application command: %w", err)
		}

		d.commandIds[CMD_ROOT_NAME] = cmd.ID
	case 1:
		err := d.db.SetDiscordVersion(ctx, tx, 2)
		if err != nil {
			return err
		}

		if userCmd, ok := d.commandIds[CMD_ROOT_NAME]; ok {
			err = s.ApplicationCommandDelete(s.State.User.ID, d.guildId, userCmd)
			if err != nil {
				return fmt.Errorf("deleting application command: %w", err)
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
			return fmt.Errorf("creating application command: %w", err)
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

	return nil
}

// respond sends an interaction response and logs a failure to do so. what names the reply in the log message.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, response *discordgo.InteractionResponse, what string) {
	if err := s.InteractionRespond(i.Interaction, response); err != nil {
		log.WithError(err).Warn("Error during discord reply: '" + what + "'")
	}
}

// respondMessage replies to the interaction with a new plain text message
func respondMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string, what string) {
	respond(s, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	}, what)
}

// respondCard replies to the interaction with the given user card, using the given response type
func respondCard(s *discordgo.Session, i *discordgo.InteractionCreate, responseType discordgo.InteractionResponseType, content string, card *controller.UserCard, what string) {
	respond(s, i, &discordgo.InteractionResponse{
		Type: responseType,
		Data: &discordgo.InteractionResponseData{
			Content:    content,
			Components: card.Components,
			Embeds:     card.Embeds,
		},
	}, what)
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
		respondMessage(s, i, "Invalid user query", "invalid user query")
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

	userCard, err := d.dc.GetDiscordUserCard(d.ctx, searchString, true, isPatchCommand)
	if err != nil {
		log.WithError(err).WithField("search", searchString).Warn("User query failed")
		respondMessage(s, i, "User query failed - check logs for more details", "user query failed")
		return
	}
	d.macCache[i.Interaction.ID] = userCard.RealMAC

	respondCard(s, i, discordgo.InteractionResponseChannelMessageWithSource, userCard.Content, userCard, "final result")
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
		return
	}

	// This is an interaction on an existing find request - lets see what we need to do...
	data := i.Interaction.MessageComponentData()
	switch data.CustomID {
	case "findVlanSelect":
		// The selection changed
		d.vlanCache[i.Interaction.Message.ID] = data.Values[0]
		respond(s, i, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		}, "find vlan select")
	case "findLogoutBtn", "findLoginBtn", "findChangeBtn", "findCancelBtn":
		d.handleCardButton(s, i, data.CustomID)
	default:
		respondMessage(s, i, "Unknown interaction request - this could be a bug", "unknown interaction request")
	}
}

// handleCardButton handles a button press on a user card: it queues the VLAN change (unless cancelled) and updates the
// card so that its controls are disabled.
func (d *Discord) handleCardButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	// We need to get the id of the original message that triggered the original interaction
	mac := d.macCache[i.Interaction.Message.Interaction.ID]

	message := ""
	if customID != "findCancelBtn" {
		// All these buttons change the VLAN. Lets see...
		var targetVLAN string
		if customID == "findLogoutBtn" {
			targetVLAN = "499"
		} else {
			var ok bool
			if targetVLAN, ok = d.vlanCache[i.Interaction.Message.ID]; !ok {
				// No vlan specified - fetch the default vlan
				targetVLAN = "1"
			}
		}

		message = "**VLAN change queued**"
		if err := d.queueVLANChange(d.ctx, mac, targetVLAN); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"mac":        mac,
				"targetVLAN": targetVLAN,
			}).Warn("VLAN change request failed")
			message = "***VLAN change failed!***"
		}
	}

	// Also, lets update the user card so that stuff is disabled...
	card, err := d.dc.GetDiscordUserCard(d.ctx, mac, false, false)
	if err != nil {
		log.WithError(err).WithField("mac", mac).Warn("Error during discord card update")
		respondMessage(s, i, "\n"+message+"\n\nUser card update failed - check logs for more details", "card update failed")
		return
	}
	respondCard(s, i, discordgo.InteractionResponseUpdateMessage, "\n"+message+"\n\n"+card.Content, card, "card update")
}

// queueVLANChange validates the requested VLAN against the VLANs of the switch the client is connected to and queues
// a job moving the client there. "1" or a VLAN unknown to the switch selects the switch's primary VLAN.
func (d *Discord) queueVLANChange(ctx context.Context, mac string, targetVLAN string) error {
	sessions, err := d.db.FindSessionsForMAC(ctx, mac)
	if err != nil {
		return fmt.Errorf("looking up sessions: %w", err)
	}
	if len(sessions) < 1 {
		return errors.New("no radius session found for the mac specified")
	}
	switchRef, err := d.db.SwitchByIP(ctx, sessions[0].SwitchIP.String())
	if err != nil {
		return err
	}

	known := false
	if targetVLAN != "1" {
		for _, vlan := range switchRef.Vlans {
			if fmt.Sprint(vlan.VlanID) == targetVLAN {
				known = true
			}
		}
	}
	if !known {
		if switchRef.PrimaryVlan == nil {
			return fmt.Errorf("primary vlan of switch %s is not among its vlans", switchRef.Hostname)
		}
		targetVLAN = fmt.Sprint(switchRef.PrimaryVlan.VlanID)
	}

	if err := d.dc.ChangeUserVLAN(ctx, mac, targetVLAN); err != nil {
		return fmt.Errorf("queueing vlan change to %s: %w", targetVLAN, err)
	}
	return nil
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
