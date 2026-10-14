package playout

import (
	"slices"
	"time"
)

// Feed receives, every tick, the audio that will play Lookahead ticks later
// (silence when nothing is queued for then). It must not retain pcm.
type Feed func(pcm []int16)

// VideoSink receives one encoded video frame at the tick its audio plays.
type VideoSink func(frame []byte, duration time.Duration) error

// VideoFrameDuration is the length of one video frame: 25 fps, two audio
// ticks.
const VideoFrameDuration = 40 * time.Millisecond

const ticksPerVideoFrame = int64(VideoFrameDuration / (20 * time.Millisecond))

// SetLookahead turns on rendered video. Each tick the player feeds the audio
// it will play `ticks` ticks later, and plays only audio it has already fed,
// so a renderer that needs time to draw a frame gets the sound ahead of
// playback: frame k drawn from the fed stream belongs to fed ticks
// [2k, 2k+1] and is sent with the audio of tick 2k (see AttachVideo).
// Audio arriving after an idle stretch starts playing `ticks` ticks after it
// is enqueued. Gain and Flush still act on what plays now. Call before Run.
func (p *Player) SetLookahead(ticks int, feed Feed, sink VideoSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ahead, p.feed, p.videoSink = ticks, feed, sink
	p.video = map[int64][]byte{}
}

// StopLookahead returns to plain playback, e.g. when the renderer is gone:
// queued audio plays straight away and video is no longer accepted.
func (p *Player) StopLookahead() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ahead, p.slots, p.fed, p.video = 0, nil, 0, nil
}

// PlayDelay is how much later than it is enqueued audio plays after an
// idle stretch: the lookahead (zero without one).
func (p *Player) PlayDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(p.ahead) * 20 * time.Millisecond
}

// AttachVideo hands over frame k of the rendered stream. Frames that are
// late are sent on the next tick rather than dropped: a video decoder needs
// every frame up to the next key frame.
func (p *Player) AttachVideo(k int64, frame []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.video != nil {
		p.video[k] = frame
	}
}

// feedNext picks the audio for the newest slot: the first queued frame not
// fed yet, or silence. Called with mu held.
func (p *Player) feedNext() []int16 {
	n := 0
	for _, it := range p.queue {
		if it.frame == nil {
			continue
		}
		if n == p.fed {
			p.fed++
			p.slots = append(p.slots, true)
			return it.frame
		}
		n++
	}
	p.slots = append(p.slots, false)
	return p.silence
}

// dueVideo pops the frames due by played slot s, oldest first. Called with
// mu held.
func (p *Player) dueVideo(s int64) [][]byte {
	if len(p.video) == 0 || s%ticksPerVideoFrame != 0 {
		return nil
	}
	upTo := s / ticksPerVideoFrame
	var keys []int64
	for k := range p.video {
		if k <= upTo {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = p.video[k]
		delete(p.video, k)
	}
	return out
}
