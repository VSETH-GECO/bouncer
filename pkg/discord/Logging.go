package discord

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	log "github.com/sirupsen/logrus"
)

type discordLogger struct {
	s          *discordgo.Session
	logChannel string
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
	var message = entry.Message
	var first = true
	for name, field := range entry.Data {
		if first {
			first = false
			message = message + "\n\n"
		} else {
			message = message + "\t"
		}
		message = message + name + ": " + fmt.Sprint(field)
	}

	// Panic is the smallest level and then it ascends
	if entry.Level <= log.ErrorLevel {
		message = "```css\n[" + message + "]\n```"
	} else if entry.Level <= log.WarnLevel {
		message = "```fix\n" + message + "```\n"
	} else {
		message = "```bash\n\"" + message + "\"```\n"
	}

	_, err := d.s.ChannelMessageSend(d.logChannel, message)
	return err
}
