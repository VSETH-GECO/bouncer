package discord

import (
	"context"
	"fmt"
	"slices"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
)

// Discord provides a discord bot interface to this app
type Discord struct {
	db *db.DB
	dc *controller.DiscordController

	// ctx is the base context for all interaction handlers. discordgo invokes handlers on its own goroutines without a
	// context, so the run context given to Setup is kept here to cancel handler queries on shutdown.
	ctx     context.Context
	session *discordgo.Session

	allowedUsers       []string
	logChannel         string
	privateInfoChannel string
	token              string
	guildId            string

	commandIds map[string]string
	vlanCache  map[string]string
	macCache   map[string]string
}

// NewDiscord returns a new discord handler configured by cfg
func NewDiscord(d *db.DB, cfg config.Discord) *Discord {
	obj := &Discord{
		db:                 d,
		ctx:                context.Background(),
		allowedUsers:       cfg.Users,
		logChannel:         cfg.LogChannel,
		privateInfoChannel: cfg.PrivateInfoChannel,
		token:              cfg.Token,
		guildId:            cfg.GuildID,
		commandIds:         map[string]string{},
		vlanCache:          map[string]string{},
		macCache:           map[string]string{},
		dc:                 controller.NewDiscordController(d),
	}
	return obj
}

// Setup connects to discord and registers all commands. If the discord config is incomplete, the bot is not started
// and Setup returns nil. Once Setup succeeded, call Close on shutdown.
func (d *Discord) Setup(ctx context.Context) error {
	if d.token == "" || d.guildId == "" || d.privateInfoChannel == "" || d.logChannel == "" {
		log.Warning("Discord config missing or incomplete")
		return nil
	}

	log.Info("Discord config detected, starting bot!")
	log.Info("Authorized users: ")
	for _, user := range d.allowedUsers {
		log.Info(user)
	}

	d.ctx = ctx
	session, err := discordgo.New("Bot " + d.token)
	if err != nil {
		return fmt.Errorf("creating discord session: %w", err)
	}

	err = session.Open()
	if err != nil {
		return fmt.Errorf("opening discord session: %w", err)
	}

	err = d.migrateCommands(ctx, session)
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("setting up discord commands: %w", err)
	}

	err = d.setupHandlers(session)
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("setting up discord handlers: %w", err)
	}

	d.session = session
	// Everything seems good
	log.AddHook(&discordLogger{
		s:          session,
		logChannel: d.logChannel,
	})
	return nil
}

// Close closes the discord session, if one was opened by Setup
func (d *Discord) Close() error {
	if d.session == nil {
		return nil
	}
	if err := d.session.Close(); err != nil {
		return fmt.Errorf("closing discord session: %w", err)
	}
	return nil
}

func (d *Discord) IsAuthorized(name string) bool {
	return slices.Contains(d.allowedUsers, name)
}
