package playout

// MotionSink receives an avatar motion payload at the moment the audio
// frame it belongs to is handed to the audio sink.
type MotionSink func(payload []byte)

type motionItem struct {
	pos     int64 // sample position on the playout timeline
	payload []byte
}

// SetMotionSink installs the motion receiver. Call before Run.
func (p *Player) SetMotionSink(s MotionSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.motionSink = s
}

// EnqueuePos is the timeline position the next enqueued sample will have.
func (p *Player) EnqueuePos() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queuedFrames*p.frameSamples + int64(p.framer.Len())
}

// AttachMotion schedules payload to be sent with the audio at pos (from
// EnqueuePos). Positions must be attached in non-decreasing order. Motion
// for audio that has already played is dropped: a stale mouth shape is
// worse than none.
func (p *Player) AttachMotion(pos int64, payload []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.motionSink == nil || pos < p.nextPlay*p.frameSamples {
		return // nobody listens, or its audio has already played
	}
	p.motion = append(p.motion, motionItem{pos: pos, payload: payload})
}

// dueMotion pops motion for frame index i. Called with mu held.
func (p *Player) dueMotion(i int64) [][]byte {
	if p.motionSink == nil || len(p.motion) == 0 {
		return nil
	}
	end := (i + 1) * p.frameSamples
	var out [][]byte
	for len(p.motion) > 0 && p.motion[0].pos < end {
		out = append(out, p.motion[0].payload)
		p.motion = p.motion[1:]
	}
	if len(p.motion) == 0 {
		p.motion = nil
	}
	return out
}
