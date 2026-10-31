package discord

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type post struct{ channel, content string }

// fakePoster records every message and fails with err if set.
type fakePoster struct {
	posts []post
	err   error
}

func (f *fakePoster) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.posts = append(f.posts, post{channelID, content})
	if f.err != nil {
		return nil, f.err
	}
	return &discordgo.Message{}, nil
}

func newTestLogger() (*slog.Logger, *fakePoster) {
	fake := &fakePoster{}
	return slog.New(newLogHandler(slog.NewTextHandler(io.Discard, nil), fake, "chan")), fake
}

func onlyPost(t *testing.T, fake *fakePoster) string {
	t.Helper()
	if len(fake.posts) != 1 {
		t.Fatalf("got %d posts, want 1: %q", len(fake.posts), fake.posts)
	}
	if fake.posts[0].channel != "chan" {
		t.Errorf("posted to channel %q, want %q", fake.posts[0].channel, "chan")
	}
	return fake.posts[0].content
}

func TestLogHandlerInfo(t *testing.T) {
	logger, fake := newTestLogger()
	logger.Info("msg", "k1", "v1", "k2", "v2")
	want := "```bash\n\"msg\n\nk1: v1\tk2: v2\"```\n"
	if got := onlyPost(t, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerInfoWithoutAttrs(t *testing.T) {
	logger, fake := newTestLogger()
	logger.Info("msg")
	want := "```bash\n\"msg\"```\n"
	if got := onlyPost(t, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerWarn(t *testing.T) {
	logger, fake := newTestLogger()
	logger.Warn("careful", "error", errors.New("boom"))
	want := "```fix\ncareful\n\nerror: boom```\n"
	if got := onlyPost(t, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerError(t *testing.T) {
	logger, fake := newTestLogger()
	logger.Error("broken", "n", 3)
	want := "```css\n[broken\n\nn: 3]\n```"
	if got := onlyPost(t, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerDebugNotPosted(t *testing.T) {
	fake := &fakePoster{}
	next := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(newLogHandler(next, fake, "chan"))
	logger.Debug("quiet", "k", "v")
	if len(fake.posts) != 0 {
		t.Errorf("Debug record was posted: %q", fake.posts)
	}
}

func TestLogHandlerWithAttrsAndGroup(t *testing.T) {
	logger, fake := newTestLogger()
	logger.With("a", 1).WithGroup("g").With("b", 2).Info("msg", "c", 3, slog.Group("h", "d", 4))
	want := "```bash\n\"msg\n\na: 1\tg.b: 2\tg.c: 3\tg.h.d: 4\"```\n"
	if got := onlyPost(t, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerForwardsToNext(t *testing.T) {
	var buf bytes.Buffer
	fake := &fakePoster{}
	logger := slog.New(newLogHandler(slog.NewTextHandler(&buf, nil), fake, "chan"))
	logger.With("a", 1).WithGroup("g").Info("msg", "c", 3)
	if out := buf.String(); !strings.Contains(out, `msg=msg a=1 g.c=3`) {
		t.Errorf("next handler got %q", out)
	}
}

func TestLogHandlerPostingError(t *testing.T) {
	var buf bytes.Buffer
	fake := &fakePoster{err: errors.New("session closed")}
	next := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(newLogHandler(next, fake, "chan"))

	logger.Info("first")
	logger.Warn("second")
	if len(fake.posts) != 2 {
		t.Fatalf("got %d post attempts, want 2 (no recursion)", len(fake.posts))
	}
	out := buf.String()
	for _, want := range []string{"msg=first", "msg=second", `level=DEBUG msg="posting log to discord failed" error="session closed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("next handler output %q lacks %q", out, want)
		}
	}
}
