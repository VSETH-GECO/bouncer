package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// poster is the part of *discordgo.Session the handler needs; a fake stands in for it in tests.
type poster interface {
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// logHandler forwards every record to next and mirrors records at Info and above into a Discord channel, formatted
// exactly as the former log hook did.
type logHandler struct {
	next    slog.Handler
	post    poster
	channel string
	// attrs are the attributes added with WithAttrs, with their keys already qualified by the groups open at the time
	attrs []slog.Attr
	// groups are the group names opened with WithGroup, outermost first
	groups []string
}

func newLogHandler(next slog.Handler, post poster, channel string) *logHandler {
	return &logHandler{next: next, post: post, channel: channel}
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.next.Handle(ctx, r); err != nil {
		return err
	}
	if r.Level < slog.LevelInfo {
		return nil
	}

	fields := append([]slog.Attr(nil), h.attrs...)
	prefix := h.prefix()
	r.Attrs(func(a slog.Attr) bool {
		fields = appendQualified(fields, prefix, a)
		return true
	})

	message := r.Message
	for i, f := range fields {
		if i == 0 {
			message = message + "\n\n"
		} else {
			message = message + "\t"
		}
		message = message + f.Key + ": " + fmt.Sprint(f.Value.Any())
	}

	switch {
	case r.Level >= slog.LevelError:
		message = "```css\n[" + message + "]\n```"
	case r.Level >= slog.LevelWarn:
		message = "```fix\n" + message + "```\n"
	default:
		message = "```bash\n\"" + message + "\"```\n"
	}

	if _, err := h.post.ChannelMessageSend(h.channel, message); err != nil {
		// Report through next only: going through this handler again would try to post the failure as well
		if h.next.Enabled(ctx, slog.LevelDebug) {
			failure := slog.NewRecord(time.Now(), slog.LevelDebug, "posting log to discord failed", 0)
			failure.AddAttrs(slog.Any("error", err))
			_ = h.next.Handle(ctx, failure)
		}
	}
	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := h.clone()
	prefix := h.prefix()
	for _, a := range attrs {
		c.attrs = appendQualified(c.attrs, prefix, a)
	}
	c.next = h.next.WithAttrs(attrs)
	return c
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := h.clone()
	c.groups = append(c.groups, name)
	c.next = h.next.WithGroup(name)
	return c
}

func (h *logHandler) clone() *logHandler {
	return &logHandler{
		next:    h.next,
		post:    h.post,
		channel: h.channel,
		attrs:   append([]slog.Attr(nil), h.attrs...),
		groups:  append([]string(nil), h.groups...),
	}
}

// prefix is the key prefix for attributes added under the currently open groups
func (h *logHandler) prefix() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".") + "."
}

// appendQualified appends a to dst with its key prefixed, flattening group values into "group.key" attributes the
// way the text handler names them. Empty attributes and empty groups are dropped, groups without a key are inlined.
func appendQualified(dst []slog.Attr, prefix string, a slog.Attr) []slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := prefix
		if a.Key != "" {
			inner = prefix + a.Key + "."
		}
		for _, g := range a.Value.Group() {
			dst = appendQualified(dst, inner, g)
		}
		return dst
	}
	a.Key = prefix + a.Key
	return append(dst, a)
}
