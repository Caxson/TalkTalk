package session

import (
	"context"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/photoreal"
	"github.com/Caxson/TalkTalk/internal/playout"
	"github.com/Caxson/TalkTalk/internal/rtc"
)

// VideoDeps configure rendered (photoreal) video. A zero Addr disables it.
type VideoDeps struct {
	Addr string        // the photoreal worker, usually an SSH tunnel
	Lead time.Duration // how far ahead of playback audio goes to it
}

// dialVideo connects a session to the photoreal worker. It returns nil, and
// the session runs voice-only, when the worker cannot be reached.
func (m *Manager) dialVideo(ctx context.Context, character string, rate int, log *slog.Logger) *photoreal.Client {
	client, err := photoreal.Dial(ctx, m.d.Video.Addr, character, rate, log)
	if err != nil {
		log.Warn("photoreal worker unavailable, voice only", "err", err)
		return nil
	}
	return client
}

// startVideo turns on the player's lookahead, so the worker gets each
// moment's audio Lead before it plays and its frames go out with that audio.
func (m *Manager) startVideo(client *photoreal.Client, player *playout.Player, peer *atomic.Pointer[rtc.Peer], log *slog.Logger) {
	ticks := int(m.d.Video.Lead / audio.FrameDuration)
	failed := false
	player.SetLookahead(ticks, client.Feed, func(au []byte, d time.Duration) error {
		p := peer.Load()
		if p == nil {
			return nil
		}
		if err := p.WriteVideo(au, d); err != nil && !failed {
			// Losing the picture must not end the conversation.
			failed = true
			log.Warn("video write failed; continuing with voice", "err", err)
		}
		return nil
	})
}

// runVideo forwards the worker's frames to the player until the session or
// the worker ends. If the worker goes first, the call carries on as plain
// voice: no lookahead delay for a picture that will not come.
func runVideo(ctx context.Context, client *photoreal.Client, player *playout.Player, log *slog.Logger) {
	started := time.Now()
	err := client.Run(ctx, photoreal.Handlers{
		OnReady: func(i photoreal.Info) {
			log.Info("photoreal ready", "after", time.Since(started).Round(time.Millisecond),
				"size", i.Width, "height", i.Height, "first_frame", i.FirstFrame)
		},
		OnFrame: player.AttachVideo,
	})
	if ctx.Err() == nil {
		log.Warn("photoreal stopped; continuing with voice", "err", err)
		client.Close()
		player.StopLookahead()
	}
}
