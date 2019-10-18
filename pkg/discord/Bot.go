package discord

import (
	"github.com/Necroforger/dgrouter/exrouter"
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Discord provides a discord bot interface to this app
type Discord struct {
	db           *database.Handler
	allowedUsers []string
}

// NewDiscord returns a new discord handler
func NewDiscord(db *database.Handler) *Discord {
	obj := &Discord{
		db: db,
	}
	return obj
}

// Setup registers all commands with discord
func (d *Discord) Setup() {
	token := viper.GetString("dtoken")
	users := viper.GetStringSlice("dusers")
	if token != "" {
		log.Info("Discord token detected, starting bot!")

		discord, err := discordgo.New("Bot " + token)
		if err != nil {
			log.WithError(err).Warning("Connection to discord failed")
			return
		}
		router := exrouter.New()

		isOk := func(name string) bool {
			for _, user := range users {
				if name == user {
					return true
				}
			}
			return false
		}

		router.On("ping", func(ctx *exrouter.Context) {
			if !isOk(ctx.Msg.Author.Username) {
				return
			}
			_, err := ctx.Reply("pong")
			if err != nil {
				log.WithError(err).Warn("Error during discord reply")
			}
		}).Desc("responds with pong")

		router.Default = router.On("help", func(ctx *exrouter.Context) {
			var text = ""
			for _, v := range router.Routes {
				text += v.Name + " : \t" + v.Description + "\n"
			}
			_, err := ctx.Reply("```" + text + "```")
			if err != nil {
				log.WithError(err).Warn("Error during discord reply")
			}
		}).Desc("prints this help menu")

		discord.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
			_ = router.FindAndExecute(discord, "/network ", discord.State.User.ID, m.Message)
		})

		err = discord.Open()
		if err != nil {
			log.WithError(err).Warning("Couldn't open discord session")
		}
		_, err = discord.ChannelMessageSend("admin", "I am awake!")
		if err != nil {
			log.WithError(err).Warning("Couldn't send message")
		}
	}
}
