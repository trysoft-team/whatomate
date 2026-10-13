package realtime

import (
	"sync"
	"time"
)

// RecordingRate is the sample rate of AI Call recordings (mono PCM16 per track).
const RecordingRate = 16000

// Timeline records caller and assistant audio on a shared wall clock, so the
// stereo mix sounds like the conversation did (pauses included).
type Timeline struct {
	mu        sync.Mutex
	start     time.Time
	now       func() time.Time
	user      []int16
	assistant []int16
}

func NewTimeline() *Timeline {
	t := &Timeline{now: time.Now}
	t.start = t.now()
	return t
}

func (t *Timeline) pos() int {
	return int(t.now().Sub(t.start).Seconds() * RecordingRate)
}

func padTo(buf []int16, n int) []int16 {
	if n > len(buf) {
		buf = append(buf, make([]int16, n-len(buf))...)
	}
	return buf
}

func (t *Timeline) WriteUser(pcm []int16) {
	if len(pcm) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pos()
	t.user = append(padTo(t.user, p), pcm...)
	t.assistant = padTo(t.assistant, p)
}

func (t *Timeline) WriteAssistant(pcm []int16) {
	if len(pcm) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pos()
	t.assistant = append(padTo(t.assistant, p), pcm...)
	t.user = padTo(t.user, p)
}

// Finalize returns both tracks padded to the same length.
func (t *Timeline) Finalize() (user, assistant []int16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := max(len(t.user), len(t.assistant), t.pos())
	return padTo(t.user, n), padTo(t.assistant, n)
}
