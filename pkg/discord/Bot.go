package discord

import (
	"fmt"
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

type discordLogger struct {
	s *discordgo.Session
}

func (d *discordLogger) Levels() []log.Level {
	return []log.Level{
		log.InfoLevel,
		log.WarnLevel,
		log.ErrorLevel,
		log.FatalLevel,
		log.PanicLevel,
	}
}

func (d *discordLogger) Fire(entry *log.Entry) error {
	msg, err := entry.String()
	if err != nil {
		return err
	}
	_, err = d.s.ChannelMessageSend("634738075011907599", msg)
	return err
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
		log.Info("Authorized users: ")
		for _, user := range users {
			log.Info(user)
		}

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

		router.On("find", func(ctx *exrouter.Context) {
			if !isOk(ctx.Msg.Author.ID) {
				log.WithFields(log.Fields{
					"id":   ctx.Msg.Author.ID,
					"name": ctx.Msg.Author.Username,
				}).Info("Unauthorized access to command blocked")
				return
			}

			var user string
			num, err := fmt.Sscanf(ctx.Msg.Content, "/network find %s", &user)
			if err == nil && num == 1 {
				mail, mac, vlan, switchIP, switchPort, hostname, ip, ok, err := d.db.FindUser(user)
				if err != nil {
					_, err := ctx.Reply("Couldn't fetch user: " + err.Error())
					if err != nil {
						log.WithError(err).Warn("Error during discord reply")
					}
					return
				} else if !ok {
					_, err := ctx.Reply("No user found with search string '" + user + "'")
					if err != nil {
						log.WithError(err).Warn("Error during discord reply")
					}
				}

				var msg string
				if ctx.Msg.ChannelID != "303183210123231243" {
					msg = fmt.Sprintf("Mail: <removed in public channel>\n"+
						"Mac: %s\n"+
						"Switch IP: %s\n"+
						"Switch Port: %s\n"+
						"VLAN: %s\n"+
						"Hostname: %s\n"+
						"Current IP: %s\n", mac, switchIP, switchPort, vlan, hostname, ip)
				} else {
					msg = fmt.Sprintf("Mail: %s\n"+
						"Mac: %s\n"+
						"Switch IP: %s\n"+
						"Switch Port: %s\n"+
						"VLAN: %s\n"+
						"Hostname: %s\n"+
						"Current IP: %s\n", mail, mac, switchIP, switchPort, vlan, hostname, ip)
				}
				_, err = ctx.Reply(msg)
				if err != nil {
					log.WithError(err).Warn("Error during discord reply")
				}
			} else if err != nil {
				_, err := ctx.Reply("Couldn't parse arguments")
				if err != nil {
					log.WithError(err).Warn("Error during discord reply")
				}
			}

		}).Desc("Finds an user by either email, hostname or mac")

		router.On("patch", func(ctx *exrouter.Context) {
			if !isOk(ctx.Msg.Author.Username) {
				log.WithFields(log.Fields{
					"id":   ctx.Msg.Author.ID,
					"name": ctx.Msg.Author.Username,
				}).Info("Unauthorized access to command blocked")
				return
			}

			var user string
			var vlan int
			num, err := fmt.Sscanf(ctx.Msg.Content, "/network patch %s %d", &user, &vlan)
			if err == nil && num == 2 {
				err := d.db.MoveHostToVLAN(-1, user, vlan)
				var msg string
				if err != nil {
					msg = err.Error()
				} else {
					msg = "Job created successfully"
				}
				_, err = ctx.Reply(msg)
				if err != nil {
					log.WithError(err).Warn("Error during discord reply")
				}
			} else if err != nil {
				_, err := ctx.Reply("Couldn't parse arguments")
				if err != nil {
					log.WithError(err).Warn("Error during discord reply")
				}
			}

		}).Desc("Moves user to another vlan")

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
		_, err = discord.ChannelMessageSend("634738075011907599", "I am awake!")
		if err != nil {
			log.WithError(err).Warning("Couldn't send message")
		} else {
			// Everything seems good
			log.AddHook(&discordLogger{
				s: discord,
			})
		}
	}
}
