package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCommandDefinition(t *testing.T) {
	c := commandDefinition()
	if c.Name != CMD_ROOT_NAME {
		t.Errorf("Name = %q, want %q", c.Name, CMD_ROOT_NAME)
	}
	if c.Description == "" {
		t.Error("Description is empty")
	}
	if c.DMPermission == nil || *c.DMPermission {
		t.Errorf("DMPermission = %v, want false", c.DMPermission)
	}
	if c.DefaultMemberPermissions == nil || *c.DefaultMemberPermissions != 0 {
		t.Errorf("DefaultMemberPermissions = %v, want 0", c.DefaultMemberPermissions)
	}
	if len(c.Options) != 3 {
		t.Fatalf("subcommands = %d, want 3", len(c.Options))
	}
	for i, name := range []string{CMD_USER_NAME, CMD_PATCH_NAME} {
		sub := c.Options[i]
		if sub.Name != name || sub.Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Errorf("option %d = %q (type %v), want subcommand %q", i, sub.Name, sub.Type, name)
		}
		if sub.Description == "" {
			t.Errorf("subcommand %q has no description", name)
		}
		if len(sub.Options) != 1 {
			t.Fatalf("subcommand %q has %d options, want 1", name, len(sub.Options))
		}
		o := sub.Options[0]
		if o.Name != "search-string" || o.Type != discordgo.ApplicationCommandOptionString || !o.Required {
			t.Errorf("subcommand %q option = %q (type %v, required %v), want required string search-string",
				name, o.Name, o.Type, o.Required)
		}
	}
}

func TestCommandDefinition_Logout(t *testing.T) {
	group := commandDefinition().Options[2]
	if group.Name != CMD_LOGOUT_NAME || group.Type != discordgo.ApplicationCommandOptionSubCommandGroup {
		t.Fatalf("option 2 = %q (type %v), want subcommand group %q", group.Name, group.Type, CMD_LOGOUT_NAME)
	}
	if group.Description == "" {
		t.Error("group has no description")
	}
	if len(group.Options) != 3 {
		t.Fatalf("logout subcommands = %d, want 3", len(group.Options))
	}
	for i, name := range []string{CMD_LOGOUT_SCHEDULE_NAME, CMD_LOGOUT_CANCEL_NAME, CMD_LOGOUT_STATUS_NAME} {
		sub := group.Options[i]
		if sub.Name != name || sub.Type != discordgo.ApplicationCommandOptionSubCommand || sub.Description == "" {
			t.Errorf("logout option %d = %q (type %v), want subcommand %q with a description", i, sub.Name, sub.Type, name)
		}
	}
	schedule := group.Options[0]
	if len(schedule.Options) != 1 || schedule.Options[0].Name != "time" ||
		schedule.Options[0].Type != discordgo.ApplicationCommandOptionString || !schedule.Options[0].Required {
		t.Errorf("schedule options = %+v, want a required string option time", schedule.Options)
	}
}
