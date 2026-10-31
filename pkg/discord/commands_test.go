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
	if len(c.Options) != 2 {
		t.Fatalf("subcommands = %d, want 2", len(c.Options))
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
