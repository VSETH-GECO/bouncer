package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type post struct{ channel, content string }

// fakePoster records every message and fails with err if set. block, if set, holds every post until it is closed and
// entered reports that a post has started.
type fakePoster struct {
	posts       []post
	attachments []string
	err         error
	block       chan struct{}
	entered     chan struct{}
}

func (f *fakePoster) wait() {
	if f.block != nil {
		f.entered <- struct{}{}
		<-f.block
	}
}

func (f *fakePoster) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.wait()
	f.posts = append(f.posts, post{channelID, content})
	if f.err != nil {
		return nil, f.err
	}
	return &discordgo.Message{}, nil
}

func (f *fakePoster) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.wait()
	f.posts = append(f.posts, post{channelID, data.Content})
	if f.err != nil {
		return nil, f.err
	}
	for _, file := range data.Files {
		b, err := io.ReadAll(file.Reader)
		if err != nil {
			return nil, err
		}
		f.attachments = append(f.attachments, string(b))
	}
	return &discordgo.Message{}, nil
}

func newTestLogger() (*slog.Logger, *logHandler, *fakePoster) {
	fake := &fakePoster{}
	h := newLogHandler(slog.NewTextHandler(io.Discard, nil), fake, "chan")
	return slog.New(h), h, fake
}

func onlyPost(t *testing.T, h *logHandler, fake *fakePoster) string {
	t.Helper()
	h.mirror.flush(context.Background())
	if len(fake.posts) != 1 {
		t.Fatalf("got %d posts, want 1: %q", len(fake.posts), fake.posts)
	}
	if fake.posts[0].channel != "chan" {
		t.Errorf("posted to channel %q, want %q", fake.posts[0].channel, "chan")
	}
	return fake.posts[0].content
}

func TestLogHandlerInfo(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Info("msg", "k1", "v1", "k2", "v2")
	want := "```bash\n\"msg\n\nk1: v1\tk2: v2\"```\n"
	if got := onlyPost(t, h, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerInfoWithoutAttrs(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Info("msg")
	want := "```bash\n\"msg\"```\n"
	if got := onlyPost(t, h, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerWarn(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Warn("careful", "error", errors.New("boom"))
	want := "```fix\ncareful\n\nerror: boom```\n"
	if got := onlyPost(t, h, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerError(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Error("broken", "n", 3)
	want := "```css\n[broken\n\nn: 3]\n```"
	if got := onlyPost(t, h, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogHandlerDebugNotPosted(t *testing.T) {
	fake := &fakePoster{}
	next := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := newLogHandler(next, fake, "chan")
	slog.New(h).Debug("quiet", "k", "v")
	h.mirror.flush(context.Background())
	if len(fake.posts) != 0 {
		t.Errorf("Debug record was posted: %q", fake.posts)
	}
}

func TestLogHandlerWithAttrsAndGroup(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.With("a", 1).WithGroup("g").With("b", 2).Info("msg", "c", 3, slog.Group("h", "d", 4))
	want := "```bash\n\"msg\n\na: 1\tg.b: 2\tg.c: 3\tg.h.d: 4\"```\n"
	if got := onlyPost(t, h, fake); got != want {
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

func TestMirrorBatchesLinesIntoOneMessage(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Info("one")
	logger.With("a", 1).Warn("two")
	want := "```bash\n\"one\"```\n" + "\n" + "```fix\ntwo\n\na: 1```\n"
	if got := onlyPost(t, h, fake); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMirrorEmptyQueuePostsNothing(t *testing.T) {
	_, h, fake := newTestLogger()
	h.mirror.flush(context.Background())
	if len(fake.posts) != 0 {
		t.Errorf("posted %q from an empty queue", fake.posts)
	}
}

func TestMirrorHandleDoesNotWaitForDiscord(t *testing.T) {
	fake := &fakePoster{block: make(chan struct{}), entered: make(chan struct{}, 8)}
	h := newLogHandler(slog.NewTextHandler(io.Discard, nil), fake, "chan")
	logger := slog.New(h)
	h.mirror.start()

	logger.Info("first")
	select {
	case <-fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the mirror never tried to post")
	}

	// The poster is stuck inside a post now. Logging must still return at once.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			logger.Info("more", "i", i)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle blocked on a stuck poster")
	}

	close(fake.block)
	h.mirror.stop()
}

func TestMirrorLongBatchGoesAsAttachment(t *testing.T) {
	logger, h, fake := newTestLogger()
	for i := range 300 {
		logger.Info("a line long enough to need many of them", "i", i)
	}
	if got, want := onlyPost(t, h, fake), "300 log lines, see the attachment"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if len(fake.attachments) != 1 {
		t.Fatalf("got %d attachments, want 1", len(fake.attachments))
	}
	for _, want := range []string{" INFO a line long enough to need many of them\n\ni: 0\n", "i: 299\n"} {
		if !strings.Contains(fake.attachments[0], want) {
			t.Errorf("attachment lacks %q", want)
		}
	}
	if strings.Contains(fake.attachments[0], "```") {
		t.Error("attachment contains code fences")
	}
}

func TestMirrorSingleOversizedRecordGoesAsAttachment(t *testing.T) {
	logger, h, fake := newTestLogger()
	logger.Error("panic", "stack", strings.Repeat("x", 2*discordMessageLimit))
	onlyPost(t, h, fake)
	if len(fake.attachments) != 1 || !strings.Contains(fake.attachments[0], " ERROR panic") {
		t.Errorf("attachments = %d, want the record as one attachment", len(fake.attachments))
	}
}

func TestMirrorFailedPostIsRetried(t *testing.T) {
	var buf bytes.Buffer
	fake := &fakePoster{err: errors.New("session closed")}
	next := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := newLogHandler(next, fake, "chan")
	logger := slog.New(h)

	logger.Info("first")
	h.mirror.flush(context.Background())
	logger.Warn("second")
	if !strings.Contains(buf.String(), `level=DEBUG msg="posting log to discord failed" error="session closed"`) {
		t.Errorf("next handler output %q lacks the failure", buf.String())
	}

	fake.err, fake.posts = nil, nil
	h.mirror.flush(context.Background())
	want := "```bash\n\"first\"```\n" + "\n" + "```fix\nsecond```\n"
	if len(fake.posts) != 1 || fake.posts[0].content != want {
		t.Errorf("posts = %q, want one post %q", fake.posts, want)
	}
}

func TestMirrorCapDropsOldestAndReportsCount(t *testing.T) {
	logger, h, fake := newTestLogger()
	for i := range maxMirrorLines + 5 {
		logger.Info(fmt.Sprintf("line %d.", i))
	}
	onlyPost(t, h, fake)
	if len(fake.attachments) != 1 {
		t.Fatalf("got %d attachments, want 1", len(fake.attachments))
	}
	file := fake.attachments[0]
	if !strings.Contains(file, "5 log lines dropped") {
		t.Error("attachment does not report the dropped lines")
	}
	if strings.Contains(file, " line 4.") {
		t.Error("attachment has a line that should have been dropped")
	}
	if !strings.Contains(file, " line 5.") || !strings.Contains(file, fmt.Sprintf(" line %d.", maxMirrorLines+4)) {
		t.Error("attachment lacks the newest lines")
	}
}

func TestMirrorStopFlushesAndStopsQueueing(t *testing.T) {
	logger, h, fake := newTestLogger()
	h.mirror.start()
	logger.Info("last words")
	h.mirror.stop()
	if len(fake.posts) != 1 || !strings.Contains(fake.posts[0].content, "last words") {
		t.Fatalf("posts = %q, want the last line flushed", fake.posts)
	}

	logger.Info("after stop")
	h.mirror.flush(context.Background())
	if len(fake.posts) != 1 {
		t.Errorf("posts = %q, want nothing queued after stop", fake.posts)
	}
}
