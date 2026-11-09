package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	// mirrorInterval is the wait between two posts to the log channel.
	mirrorInterval = time.Second
	// maxMirrorLines caps the lines waiting to be posted, so a long Discord outage cannot use unlimited memory.
	maxMirrorLines = 10000
	// discordMessageLimit is the longest message content Discord accepts, in characters. Comparing it with a length
	// in bytes errs on the safe side.
	discordMessageLimit = 2000
	// mirrorFlushTimeout bounds the last post when the mirror stops.
	mirrorFlushTimeout = 2 * time.Second
	// mirrorAttachmentName names the file carrying a batch too long for a message.
	mirrorAttachmentName = "bouncer-log.txt"
)

// poster is the part of *discordgo.Session the mirror needs; a fake stands in for it in tests.
type poster interface {
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// logLine is one record waiting to be mirrored.
type logLine struct {
	at    time.Time
	level slog.Level
	text  string
}

// fenced renders the line for a message, formatted exactly as the former log hook did.
func (l logLine) fenced() string {
	switch {
	case l.level >= slog.LevelError:
		return "```css\n[" + l.text + "]\n```"
	case l.level >= slog.LevelWarn:
		return "```fix\n" + l.text + "```\n"
	default:
		return "```bash\n\"" + l.text + "\"```\n"
	}
}

// plain renders the line for an attachment.
func (l logLine) plain() string {
	return l.at.UTC().Format(time.RFC3339) + " " + l.level.String() + " " + l.text + "\n"
}

// logMirror posts queued log lines to a Discord channel, at most one message per mirrorInterval, each carrying
// everything queued since the last. Only its own goroutine ever waits on Discord.
type logMirror struct {
	post    poster
	channel string
	// next receives the report of a failed post. Going through the logHandler would queue the failure as well.
	next slog.Handler

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex // guards the fields below
	lines   []logLine
	dropped int
	stopped bool
}

// add queues a line, dropping the oldest when the queue is full. It never waits on Discord.
func (m *logMirror) add(l logLine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.lines = append(m.lines, l)
	m.trim()
}

// trim drops the oldest lines beyond the cap. The caller holds m.mu.
func (m *logMirror) trim() {
	if over := len(m.lines) - maxMirrorLines; over > 0 {
		m.lines = m.lines[over:]
		m.dropped += over
	}
}

// flush posts everything queued as one message. After a failed post the batch goes back to the front of the queue.
func (m *logMirror) flush(ctx context.Context) {
	m.mu.Lock()
	lines, dropped := m.lines, m.dropped
	m.lines, m.dropped = nil, 0
	m.mu.Unlock()
	if len(lines) == 0 && dropped == 0 {
		return
	}

	err := m.send(ctx, lines, dropped)
	if err == nil {
		return
	}
	if m.next.Enabled(ctx, slog.LevelDebug) {
		failure := slog.NewRecord(time.Now(), slog.LevelDebug, "posting log to discord failed", 0)
		failure.AddAttrs(slog.Any("error", err))
		_ = m.next.Handle(ctx, failure)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(lines, m.lines...)
	m.dropped += dropped
	m.trim()
}

// send posts a batch: as a message if it fits, otherwise as a text attachment.
func (m *logMirror) send(ctx context.Context, lines []logLine, dropped int) error {
	opt := discordgo.WithContext(ctx)
	notice := ""
	if dropped > 0 {
		notice = fmt.Sprintf("%d log lines dropped while the queue was full", dropped)
	}

	parts := make([]string, 0, len(lines)+1)
	if notice != "" {
		parts = append(parts, logLine{level: slog.LevelWarn, text: notice}.fenced())
	}
	for _, l := range lines {
		parts = append(parts, l.fenced())
	}
	if content := strings.Join(parts, "\n"); len(content) <= discordMessageLimit {
		_, err := m.post.ChannelMessageSend(m.channel, content, opt)
		return err
	}

	var file strings.Builder
	if notice != "" {
		file.WriteString(notice + "\n")
	}
	for _, l := range lines {
		file.WriteString(l.plain())
	}
	_, err := m.post.ChannelMessageSendComplex(m.channel, &discordgo.MessageSend{
		Content: fmt.Sprintf("%d log lines, see the attachment", len(lines)),
		Files:   []*discordgo.File{{Name: mirrorAttachmentName, ContentType: "text/plain", Reader: strings.NewReader(file.String())}},
	}, opt)
	return err
}

// start starts the goroutine that posts once per mirrorInterval.
func (m *logMirror) start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		t := time.NewTicker(mirrorInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.flush(ctx)
			}
		}
	}()
}

// stop ends the goroutine, posts what is left and stops queueing.
func (m *logMirror) stop() {
	m.cancel()
	<-m.done
	ctx, cancel := context.WithTimeout(context.Background(), mirrorFlushTimeout)
	defer cancel()
	m.flush(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	m.lines, m.dropped = nil, 0
}

// logHandler forwards every record to next and hands records at Info and above to a mirror, which posts them to a
// Discord channel.
type logHandler struct {
	next   slog.Handler
	mirror *logMirror
	// attrs are the attributes added with WithAttrs, with their keys already qualified by the groups open at the time
	attrs []slog.Attr
	// groups are the group names opened with WithGroup, outermost first
	groups []string
}

func newLogHandler(next slog.Handler, post poster, channel string) *logHandler {
	return &logHandler{next: next, mirror: &logMirror{post: post, channel: channel, next: next}}
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

	h.mirror.add(logLine{at: r.Time, level: r.Level, text: message})
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
		next:   h.next,
		mirror: h.mirror,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
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
