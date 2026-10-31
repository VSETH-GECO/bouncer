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

// messageResponse is a reply with a new plain text message
func messageResponse(content string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	}
}

// cardResponse is a reply of the given type showing the given user card below content
func cardResponse(responseType discordgo.InteractionResponseType, content string, card *controller.UserCard) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: responseType,
		Data: &discordgo.InteractionResponseData{
			Content:    content,
			Components: card.Components,
			Embeds:     card.Embeds,
		},
	}
}

const (
	msgInvalidQuery       = "Invalid user query"
	msgQueryFailed        = "User query failed - check logs for more details"
	msgUnknownInteraction = "Unknown interaction request - this could be a bug"
	msgCardUpdateFailed   = "User card update failed - check logs for more details"
	msgVLANChangeQueued   = "**VLAN change queued**"
	msgVLANChangeFailed   = "***VLAN change failed!***"
)

// logoutVLAN is the VLAN a client is moved to when it is logged out.
const logoutVLAN = 499

// userCommandResponse answers the user and patch subcommands: it looks up the client matching search and shows its
// card, the long one with enabled controls for the patch command. what names the reply in the log.
func (d *Discord) userCommandResponse(ctx context.Context, search string, isPatchCommand bool) (resp *discordgo.InteractionResponse, what string) {
	if len(search) < 3 {
		return messageResponse(msgInvalidQuery), "invalid user query"
	}
	card, err := d.dc.GetDiscordUserCard(ctx, search, controller.CardOptions{Buttons: true, Long: isPatchCommand})
	if err != nil {
		slog.Warn("User query failed", "error", err, "search", search)
		return messageResponse(msgQueryFailed), "user query failed"
	}
	return cardResponse(discordgo.InteractionResponseChannelMessageWithSource, card.Content, card), "final result"
}

// componentResponse answers a click on a control of a user card. The control's CustomID carries the client's MAC
// (and, for the change button, the VLAN chosen in the menu); values are the values chosen in a select menu. what
// names the reply in the log.
func (d *Discord) componentResponse(ctx context.Context, customID string, values []string) (resp *discordgo.InteractionResponse, what string) {
	id, err := controller.ParseCardID(customID)
	if err != nil {
		slog.Warn("Unknown card interaction", "error", err)
		return messageResponse(msgUnknownInteraction), "unknown interaction request"
	}
	if id.Action == controller.ActionSelectVLAN {
		return d.vlanSelectResponse(ctx, id, values)
	}
	return d.cardButtonResponse(ctx, id)
}

// vlanSelectResponse re-renders the card with the VLAN chosen in its menu, so that the menu shows the choice and the
// change and login buttons carry it.
func (d *Discord) vlanSelectResponse(ctx context.Context, id controller.CardID, values []string) (*discordgo.InteractionResponse, string) {
	vlan, err := selectedVLAN(values)
	if err != nil {
		slog.Warn("Unknown VLAN selection", "error", err, "mac", id.MAC)
		return messageResponse(msgUnknownInteraction), "unknown interaction request"
	}
	card, err := d.dc.GetDiscordUserCard(ctx, id.MAC.Hex(), controller.CardOptions{Buttons: true, Long: true, SelectedVLAN: vlan})
	if err != nil {
		slog.Warn("Error during discord card update", "error", err, "mac", id.MAC)
		return messageResponse(msgCardUpdateFailed), "card update failed"
	}
	return cardResponse(discordgo.InteractionResponseUpdateMessage, card.Content, card), "find vlan select"
}

// selectedVLAN parses the value chosen in a card's VLAN menu.
func selectedVLAN(values []string) (int, error) {
	if len(values) != 1 {
		return 0, fmt.Errorf("want one selected value, got %d", len(values))
	}
	vlan, err := strconv.Atoi(values[0])
	if err != nil || vlan <= 0 {
		return 0, fmt.Errorf("bad VLAN value %q", values[0])
	}
	return vlan, nil
}

// requestedVLAN returns the VLAN a button press on a card asks for, and false for a control that changes nothing
// (cancel, or the menu). 0 means the primary VLAN of the client's switch.
func requestedVLAN(id controller.CardID) (vlan int, change bool) {
	switch id.Action {
	case controller.ActionLogout:
		return logoutVLAN, true
	case controller.ActionChange, controller.ActionLogin:
		// Both carry the VLAN chosen in the menu, 0 if none was chosen.
		return id.VLAN, true
	default:
		return 0, false
	}
}

// cardButtonResponse handles a button press on a user card: it queues the VLAN change (unless cancelled) and updates
// the card so that its controls are disabled.
func (d *Discord) cardButtonResponse(ctx context.Context, id controller.CardID) (*discordgo.InteractionResponse, string) {
	message := ""
	if vlan, change := requestedVLAN(id); change {
		message = msgVLANChangeQueued
		if err := d.queueVLANChange(ctx, id.MAC, vlan); err != nil {
			slog.Warn("VLAN change request failed", "error", err, "mac", id.MAC, "targetVLAN", vlan)
			message = msgVLANChangeFailed
		}
	}

	// Also, lets update the user card so that stuff is disabled...
	card, err := d.dc.GetDiscordUserCard(ctx, id.MAC.Hex(), controller.CardOptions{})
	if err != nil {
		slog.Warn("Error during discord card update", "error", err, "mac", id.MAC)
		return messageResponse("\n" + message + "\n\n" + msgCardUpdateFailed), "card update failed"
	}
	return cardResponse(discordgo.InteractionResponseUpdateMessage, "\n"+message+"\n\n"+card.Content, card), "card update"
}

// queueVLANChange validates the requested VLAN against the VLANs of the switch the client is connected to and queues
// a job moving the client there. 0 or a VLAN unknown to the switch selects the switch's primary VLAN.
func (d *Discord) queueVLANChange(ctx context.Context, m mac.MAC, vlan int) error {
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
	vlan, err = resolveVLAN(switchRef, vlan)
	if err != nil {
		return err
	}
	if err := d.dc.ChangeUserVLAN(ctx, m, vlan); err != nil {
		return fmt.Errorf("queueing vlan change to %d: %w", vlan, err)
	}
	return nil
}

// resolveVLAN returns vlan if sw has it, else sw's primary VLAN (so 0 asks for the primary VLAN).
func resolveVLAN(sw switches.Switch, vlan int) (int, error) {
	if vlan != 0 {
		for _, v := range sw.VLANs {
			if v.VLANID == vlan {
				return vlan, nil
			}
		}
	}
	if sw.PrimaryVLAN == nil {
		return 0, fmt.Errorf("primary vlan of switch %s is not among its vlans", sw.Hostname)
	}
	return sw.PrimaryVLAN.VLANID, nil
}

// handleUserSubcommand answers the user subcommand, or the patch subcommand if isPatchCommand.
func (d *Discord) handleUserSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, isPatchCommand bool) {
	var user = i.Interaction.Member.User
	if !d.IsAuthorized(user.ID) {
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
		return
	}
	searchString := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	resp, what := d.userCommandResponse(d.ctx, searchString, isPatchCommand)
	respond(s, i, resp, what)
}

// handleCardComponent answers a click on a control of a user card.
func (d *Discord) handleCardComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user = i.Interaction.Member.User
	if !d.IsAuthorized(user.ID) {
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
		return
	}
	data := i.Interaction.MessageComponentData()
	resp, what := d.componentResponse(d.ctx, data.CustomID, data.Values)
	respond(s, i, resp, what)
}

func (d *Discord) setupHandlers(s *discordgo.Session) error {
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type == discordgo.InteractionApplicationCommand {
			if i.ApplicationCommandData().Name == CMD_ROOT_NAME && i.ApplicationCommandData().Options[0].Name == CMD_USER_NAME {
				// We're supposed to find someone!
				d.handleUserSubcommand(s, i, false)
			} else if i.ApplicationCommandData().Name == CMD_ROOT_NAME && i.ApplicationCommandData().Options[0].Name == CMD_PATCH_NAME {
				// We're supposed to patch someone!
				d.handleUserSubcommand(s, i, true)
			}
		} else if i.Type == discordgo.InteractionMessageComponent {
			if strings.HasPrefix(i.Interaction.MessageComponentData().CustomID, "find") {
				d.handleCardComponent(s, i)
			}
		}
	})

	return nil
}
