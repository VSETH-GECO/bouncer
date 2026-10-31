package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
)

const (
	CMD_ROOT_NAME  = "net"
	CMD_USER_NAME  = "user"
	CMD_PATCH_NAME = "patch"
)

// commandDefinition returns the application command tree the bot registers: the root command with the user and
// patch subcommands, each taking a required search string. It is hidden from everyone but administrators (default
// member permissions 0) and unavailable in DMs.
func commandDefinition() *discordgo.ApplicationCommand {
	dmPermission := false
	var defaultMemberPermissions int64 = 0
	searchOption := func() []*discordgo.ApplicationCommandOption {
		return []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "search-string",
				Description: "The string to search for",
				Required:    true,
			},
		}
	}
	return &discordgo.ApplicationCommand{
		Name:                     CMD_ROOT_NAME,
		Description:              "PolyLAN network management commands",
		DMPermission:             &dmPermission,
		DefaultMemberPermissions: &defaultMemberPermissions,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        CMD_USER_NAME,
				Description: "Finds an user by name, email, mac or current ip",
				Options:     searchOption(),
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        CMD_PATCH_NAME,
				Description: "Finds an user by name, email, mac or current ip and enters a new patch job into the database",
				Options:     searchOption(),
			},
		},
	}
}

// registerCommands replaces the guild's application commands with commandDefinition. The bulk overwrite is
// idempotent: commands that already match are left alone and stale ones are removed.
func (d *Discord) registerCommands(s *discordgo.Session) error {
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, d.guildId, []*discordgo.ApplicationCommand{commandDefinition()})
	if err != nil {
		return fmt.Errorf("registering application commands: %w", err)
	}
	return nil
}

// respond sends an interaction response and logs a failure to do so. what names the reply in the log message.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, response *discordgo.InteractionResponse, what string) {
	if err := s.InteractionRespond(i.Interaction, response); err != nil {
		slog.Warn("Error during discord reply: '"+what+"'", "error", err)
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
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
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
		slog.Warn("User query failed", "error", err, "search", searchString)
		respondMessage(s, i, "User query failed - check logs for more details", "user query failed")
		return
	}
	d.macCache[i.Interaction.ID] = userCard.RealMAC

	respondCard(s, i, discordgo.InteractionResponseChannelMessageWithSource, userCard.Content, userCard, "final result")
}

func (d *Discord) handlePatchSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, newOrExisting bool) {
	var user = i.Interaction.Member.User

	if !d.IsAuthorized(user.ID) {
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
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
	cachedMAC := d.macCache[i.Interaction.Message.Interaction.ID]

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
		if err := d.queueVLANChange(d.ctx, cachedMAC, targetVLAN); err != nil {
			slog.Warn("VLAN change request failed", "error", err, "mac", cachedMAC, "targetVLAN", targetVLAN)
			message = "***VLAN change failed!***"
		}
	}

	// Also, lets update the user card so that stuff is disabled...
	card, err := d.dc.GetDiscordUserCard(d.ctx, cachedMAC, false, false)
	if err != nil {
		slog.Warn("Error during discord card update", "error", err, "mac", cachedMAC)
		respondMessage(s, i, "\n"+message+"\n\nUser card update failed - check logs for more details", "card update failed")
		return
	}
	respondCard(s, i, discordgo.InteractionResponseUpdateMessage, "\n"+message+"\n\n"+card.Content, card, "card update")
}

// queueVLANChange validates the requested VLAN against the VLANs of the switch the client is connected to and queues
// a job moving the client there. "1" or a VLAN unknown to the switch selects the switch's primary VLAN.
func (d *Discord) queueVLANChange(ctx context.Context, macString string, targetVLAN string) error {
	m, err := mac.Parse(macString)
	if err != nil {
		return fmt.Errorf("parsing mac %q: %w", macString, err)
	}
	sessions, err := freeradius.SessionsForMAC(ctx, d.db.Querier(), m)
	if err != nil {
		return fmt.Errorf("looking up sessions: %w", err)
	}
	if len(sessions) < 1 {
		return errors.New("no radius session found for the mac specified")
	}
	switchRef, err := switches.ByIP(ctx, d.db.Querier(), sessions[0].SwitchIP)
	if err != nil {
		return err
	}

	vlan, err := strconv.Atoi(targetVLAN)
	if err != nil {
		return fmt.Errorf("parsing target vlan %q: %w", targetVLAN, err)
	}

	known := false
	if vlan != 1 {
		for _, v := range switchRef.VLANs {
			if v.VLANID == vlan {
				known = true
			}
		}
	}
	if !known {
		if switchRef.PrimaryVLAN == nil {
			return fmt.Errorf("primary vlan of switch %s is not among its vlans", switchRef.Hostname)
		}
		vlan = switchRef.PrimaryVLAN.VLANID
	}

	if err := d.dc.ChangeUserVLAN(ctx, m, vlan); err != nil {
		return fmt.Errorf("queueing vlan change to %d: %w", vlan, err)
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
