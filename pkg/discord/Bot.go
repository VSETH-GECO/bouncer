package discord

import (
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
)

// Discord provides a discord bot interface to this app
type Discord struct {
	db *database.Handler
	dc *controller.DiscordController

	allowedUsers       []string
	logChannel         string
	privateInfoChannel string
	token              string
	guildId            string

	commandIds map[string]string
	vlanCache  map[string]string
	macCache   map[string]string
}

// NewDiscord returns a new discord handler
func NewDiscord(db *database.Handler) *Discord {
	obj := &Discord{
		db:                 db,
		allowedUsers:       config.CurrentOptions.DiscordUsers,
		logChannel:         config.CurrentOptions.DiscordLogChannel,
		privateInfoChannel: config.CurrentOptions.DiscordPrivateInfoChannel,
		token:              config.CurrentOptions.DiscordToken,
		guildId:            config.CurrentOptions.DiscordGuildID,
		commandIds:         map[string]string{},
		vlanCache:          map[string]string{},
		macCache:           map[string]string{},
		dc:                 controller.NewDiscordController(db),
	}
	return obj
}

// Setup registers all commands with discord
func (d *Discord) Setup() {
	if d.token != "" && d.guildId != "" && d.privateInfoChannel != "" && d.logChannel != "" {
		log.Info("Discord config detected, starting bot!")
		log.Info("Authorized users: ")
		for _, user := range d.allowedUsers {
			log.Info(user)
		}

		discord, err := discordgo.New("Bot " + d.token)
		if err != nil {
			log.WithError(err).Warning("Connection to discord failed")
			return
		}

		err = discord.Open()
		if err != nil {
			log.WithError(err).Warning("Couldn't open discord session")
		}

		err = d.migrateCommands(discord)
		if err != nil {
			log.WithError(err).Fatal("Couldn't setup discord commands")
		}

		err = d.setupHandlers(discord)
		if err != nil {
			log.WithError(err).Fatal("Couldn't setup discord handlers")
		}

		err = nil
		//_, err = discord.ChannelMessageSend(d.logChannel, "I am awake!")
		if err != nil {
			log.WithError(err).Warning("Couldn't send message")
		} else {
			// Everything seems good
			log.AddHook(&discordLogger{
				s:          discord,
				logChannel: d.logChannel,
			})
		}
	} else {
		log.Warning("Discord config missing or incomplete")
	}
}

func (d *Discord) IsAuthorized(name string) bool {
	for _, user := range d.allowedUsers {
		if name == user {
			return true
		}
	}
	return false
}
