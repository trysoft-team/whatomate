// Package realtime holds the audio plumbing and Gemini Live client used by
// realtime AI Calls: Opus <-> PCM, resampling, transcripts and recordings.
package realtime

import (
	"bytes"
	"encoding/binary"
	"math"
)

// Decimator low-pass filters and downsamples mono PCM16 by an integer factor.
// It keeps filter history between calls, so a stream can be fed frame by frame.
type Decimator struct {
	factor int
	taps   []float64
	hist   []float64
	phase  int
}

// NewDecimator returns a decimator whose anti-alias cutoff sits just below the
// output Nyquist frequency.
func NewDecimator(factor int) *Decimator {
	const numTaps = 47
	cutoff := 0.45 / float64(factor) // fraction of the input sample rate
	taps := make([]float64, numTaps)
	mid := float64(numTaps-1) / 2
	sum := 0.0
	for i := range taps {
		x := float64(i) - mid
		sinc := 2 * cutoff
		if x != 0 {
			sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		window := 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(numTaps-1))
		taps[i] = sinc * window
		sum += taps[i]
	}
	for i := range taps {
		taps[i] /= sum
	}
	return &Decimator{factor: factor, taps: taps, hist: make([]float64, numTaps-1)}
}

// Process consumes in and returns the decimated samples it produces.
func (d *Decimator) Process(in []int16) []int16 {
	buf := make([]float64, len(d.hist)+len(in))
	copy(buf, d.hist)
	for i, s := range in {
		buf[len(d.hist)+i] = float64(s)
	}
	out := make([]int16, 0, len(in)/d.factor+1)
	for i := len(d.hist); i < len(buf); i++ {
		if d.phase == 0 {
			acc := 0.0
			for k, t := range d.taps {
				acc += t * buf[i-k]
			}
			out = append(out, clamp16(acc))
		}
		d.phase = (d.phase + 1) % d.factor
	}
	copy(d.hist, buf[len(buf)-len(d.hist):])
	return out
}

// Upsampler2x doubles the sample rate with linear interpolation, carrying the
// last sample across calls so frame boundaries stay continuous.
type Upsampler2x struct {
	last    int16
	started bool
}

// Process returns two output samples per input sample.
func (u *Upsampler2x) Process(in []int16) []int16 {
	out := make([]int16, 0, len(in)*2)
	for _, s := range in {
		prev := s
		if u.started {
			prev = u.last
		}
		out = append(out, int16((int32(prev)+int32(s))/2), s)
		u.last = s
		u.started = true
	}
	return out
}

// RMS returns the root-mean-square level of pcm.
func RMS(pcm []int16) int {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, s := range pcm {
		v := float64(s)
		sum += v * v
	}
	return int(math.Sqrt(sum / float64(len(pcm))))
}

// SoftLimit clips pcm in place to ±limit to avoid Opus splatter on
// near-full-scale model audio.
func SoftLimit(pcm []int16, limit int16) {
	for i, s := range pcm {
		if s > limit {
			pcm[i] = limit
		} else if s < -limit {
			pcm[i] = -limit
		}
	}
}

// BytesToPCM16 decodes little-endian PCM16; a trailing odd byte is dropped.
func BytesToPCM16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// PCM16ToBytes encodes samples as little-endian PCM16.
func PCM16ToBytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

// Interleave merges two mono tracks into stereo, padding the shorter with silence.
func Interleave(left, right []int16) []int16 {
	n := max(len(left), len(right))
	out := make([]int16, n*2)
	for i := 0; i < n; i++ {
		if i < len(left) {
			out[i*2] = left[i]
		}
		if i < len(right) {
			out[i*2+1] = right[i]
		}
	}
	return out
}

// WAV wraps interleaved PCM16 samples in a RIFF/WAVE container.
func WAV(pcm []int16, sampleRate, channels int) []byte {
	dataSize := len(pcm) * 2
	var buf bytes.Buffer
	buf.Grow(44 + dataSize)
	write := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }
	buf.WriteString("RIFF")
	write(uint32(36 + dataSize))
	buf.WriteString("WAVEfmt ")
	write(uint32(16))
	write(uint16(1))
	write(uint16(channels))
	write(uint32(sampleRate))
	write(uint32(sampleRate * channels * 2))
	write(uint16(channels * 2))
	write(uint16(16))
	buf.WriteString("data")
	write(uint32(dataSize))
	buf.Write(PCM16ToBytes(pcm))
	return buf.Bytes()
}

func clamp16(v float64) int16 {
	v = math.Round(v)
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}
