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
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
)

const (
	// logoutCancelPrefix starts the CustomID of the Cancel button on a warning; the logout's id follows.
	logoutCancelPrefix = "logoutCancel:"
	msgLogoutFailed    = "The global logout request failed - check logs for more details"
)

// errNoSession reports a message that could not be posted because the bot is not running.
var errNoSession = errors.New("discord bot is not running")

// logoutCancelID returns the CustomID of the Cancel button of the logout with the given id.
func logoutCancelID(id int) string {
	return logoutCancelPrefix + strconv.Itoa(id)
}

// mentions renders Discord IDs as mentions.
func mentions(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "<@"+id+">")
	}
	return strings.Join(out, ", ")
}

// Notify posts a status message about a global logout to the private info channel.
func (d *Discord) Notify(ctx context.Context, message string) error {
	if d.session == nil {
		return errNoSession
	}
	if _, err := d.session.ChannelMessageSend(d.privateInfoChannel, message, discordgo.WithContext(ctx)); err != nil {
		return fmt.Errorf("posting a global logout message: %w", err)
	}
	return nil
}

// logoutErrorResponse answers a request the controller turned down or failed on.
func logoutErrorResponse(err error) (*discordgo.InteractionResponse, string) {
	var refusal *controller.LogoutRefusal
	if errors.As(err, &refusal) {
		return messageResponse(refusal.Message), "logout request refused"
	}
	slog.Warn("Global logout request failed", "error", err)
	return messageResponse(msgLogoutFailed), "logout request failed"
}

// logoutCommandResponse answers a subcommand of the logout group: sub names it, and value is the time it was given, if
// any. what names the reply in the log.
func (d *Discord) logoutCommandResponse(ctx context.Context, admin, sub, value string) (resp *discordgo.InteractionResponse, what string) {
	switch sub {
	case CMD_LOGOUT_SCHEDULE_NAME:
		return d.logoutScheduleResponse(ctx, admin, value)
	case CMD_LOGOUT_CANCEL_NAME:
		return d.logoutCancelResponse(ctx, admin, 0)
	case CMD_LOGOUT_STATUS_NAME:
		return d.logoutStatusResponse(ctx)
	default:
		slog.Warn("Unknown global logout subcommand", "subcommand", sub)
		return messageResponse(msgUnknownInteraction), "unknown interaction request"
	}
}

func (d *Discord) logoutScheduleResponse(ctx context.Context, admin, value string) (*discordgo.InteractionResponse, string) {
	at, err := d.logouts.ParseTime(value)
	if err != nil {
		return logoutErrorResponse(err)
	}
	res, err := d.logouts.Schedule(ctx, admin, at)
	if err != nil {
		return logoutErrorResponse(err)
	}

	when := controller.FormatLogoutTime(res.Logout.ScheduledFor)
	if res.Armed {
		slog.Warn("Global logout armed", "logoutID", res.Logout.ID, "scheduledFor", res.Logout.ScheduledFor, "approvers", res.Approvers)
		return messageResponse(fmt.Sprintf(
			"**Global logout armed** for %s, approved by %s. Warnings follow in the private channel; `/net logout cancel` stops it.",
			when, mentions(res.Approvers))), "logout armed"
	}

	slog.Warn("Global logout proposed", "logoutID", res.Logout.ID, "scheduledFor", res.Logout.ScheduledFor, "admin", admin)
	message := fmt.Sprintf(
		"**Global logout proposed** for %s by %s. A second admin has to run `/net logout schedule` with the same time at least 1 hour before it, or the proposal expires.",
		when, mentions([]string{admin}))
	// Not on the way to the reply: Discord drops an interaction not answered within 3 seconds.
	go func() {
		if err := d.Notify(ctx, message); err != nil {
			slog.Debug("Couldn't announce the global logout proposal", "error", err)
		}
	}()
	return messageResponse(message), "logout proposed"
}

func (d *Discord) logoutCancelResponse(ctx context.Context, admin string, id int) (*discordgo.InteractionResponse, string) {
	before, err := d.logouts.Cancel(ctx, admin, id)
	if err != nil {
		return logoutErrorResponse(err)
	}
	slog.Warn("Global logout cancelled", "logoutID", before.ID, "scheduledFor", before.ScheduledFor, "admin", admin, "state", before.State)
	message := fmt.Sprintf("**Global logout cancelled** by %s.", mentions([]string{admin}))
	if before.State == logout.StateRunning {
		message += " The lockout has ended. Clients already moved stay logged out."
	}
	return messageResponse(message), "logout cancelled"
}

func (d *Discord) logoutStatusResponse(ctx context.Context) (*discordgo.InteractionResponse, string) {
	status, ok, err := d.logouts.Status(ctx)
	if err != nil {
		return logoutErrorResponse(err)
	}
	if !ok {
		return messageResponse("No global logout is in flight."), "logout status"
	}
	return messageResponse(fmt.Sprintf("Global logout **%s** for %s, approved by %s. It would move %d client(s) right now.",
		status.Logout.State, controller.FormatLogoutTime(status.Logout.ScheduledFor), mentions(status.Approvers), status.Clients)), "logout status"
}

// logoutCancelButtonResponse answers a click on the Cancel button of a warning.
func (d *Discord) logoutCancelButtonResponse(ctx context.Context, admin, customID string) (*discordgo.InteractionResponse, string) {
	raw, ok := strings.CutPrefix(customID, logoutCancelPrefix)
	id, err := strconv.Atoi(raw)
	if !ok || err != nil || id <= 0 {
		slog.Warn("Unknown global logout interaction", "customID", customID)
		return messageResponse(msgUnknownInteraction), "unknown interaction request"
	}
	return d.logoutCancelResponse(ctx, admin, id)
}

// handleLogoutCommand answers a subcommand of the logout group.
func (d *Discord) handleLogoutCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user = i.Interaction.Member.User
	if !d.IsAuthorized(user.ID) {
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
		return
	}
	sub := i.ApplicationCommandData().Options[0].Options[0]
	value := ""
	if len(sub.Options) > 0 {
		value = sub.Options[0].StringValue()
	}
	resp, what := d.logoutCommandResponse(d.ctx, user.ID, sub.Name, value)
	respond(s, i, resp, what)
}

// handleLogoutCancelButton answers a click on the Cancel button of a warning.
func (d *Discord) handleLogoutCancelButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user = i.Interaction.Member.User
	if !d.IsAuthorized(user.ID) {
		slog.Info("Unauthorized access to command blocked", "id", user.ID, "name", user.Username)
		return
	}
	resp, what := d.logoutCancelButtonResponse(d.ctx, user.ID, i.Interaction.MessageComponentData().CustomID)
	respond(s, i, resp, what)
}

// warningMessage renders a warning: it pings admins, says what is about to happen and carries the Cancel button.
func warningMessage(w controller.LogoutWarning, admins []string) *discordgo.MessageSend {
	return &discordgo.MessageSend{
		Content: fmt.Sprintf(
			"%s\n**Global logout** <t:%d:R>, at %s, approved by %s.\nIt would move %d client(s) to VLAN %d right now, and moves to other VLANs are ignored for the 15 minutes after it.",
			mentions(admins), w.Logout.ScheduledFor.Unix(), controller.FormatLogoutTime(w.Logout.ScheduledFor),
			mentions(w.Approvers), w.Clients, freeradius.LoginVLAN),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Cancel logout",
						Style:    discordgo.DangerButton,
						CustomID: logoutCancelID(w.Logout.ID),
					},
				},
			},
		},
	}
}

// Warn posts a warning about a global logout to the private info channel.
func (d *Discord) Warn(ctx context.Context, w controller.LogoutWarning) error {
	if d.session == nil {
		return errNoSession
	}
	_, err := d.session.ChannelMessageSendComplex(d.privateInfoChannel, warningMessage(w, d.allowedUsers), discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("posting a global logout warning: %w", err)
	}
	return nil
}
