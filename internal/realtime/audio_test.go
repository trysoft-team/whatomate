package realtime

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sine(freq float64, rate, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

// decimateInFrames feeds the decimator 20 ms at a time, like the uplink does.
func decimateInFrames(d *Decimator, in []int16) []int16 {
	var out []int16
	for i := 0; i+OpusFrameSamples <= len(in); i += OpusFrameSamples {
		out = append(out, d.Process(in[i:i+OpusFrameSamples])...)
	}
	return out
}

func TestDecimator_PassesSpeechBandAndRejectsAliases(t *testing.T) {
	const n = OpusRate // 1 s
	pass := decimateInFrames(NewDecimator(3), sine(1000, OpusRate, n, 10000))
	require.Len(t, pass, n/3)
	assert.InDelta(t, 10000/math.Sqrt2, float64(RMS(pass[200:])), 300, "1 kHz must pass at unity gain")

	// 12 kHz would fold to 4 kHz at 16 kHz without the anti-alias filter.
	stop := decimateInFrames(NewDecimator(3), sine(12000, OpusRate, n, 10000))
	assert.Less(t, RMS(stop[200:]), 100, "12 kHz must be filtered out")
}

func TestUpsampler2x_ContinuousAcrossFrames(t *testing.T) {
	u := &Upsampler2x{}
	a := u.Process([]int16{0, 100})
	b := u.Process([]int16{200})
	assert.Equal(t, []int16{0, 0, 50, 100}, a)
	assert.Equal(t, []int16{150, 200}, b, "first output of a frame interpolates from the previous frame")
}

func TestRMSAndSoftLimit(t *testing.T) {
	assert.Equal(t, 0, RMS(nil))
	assert.Equal(t, 300, RMS([]int16{300, -300, 300, -300}))

	pcm := []int16{32000, -32000, 100}
	SoftLimit(pcm, 28000)
	assert.Equal(t, []int16{28000, -28000, 100}, pcm)
}

func TestPCMBytesRoundTrip(t *testing.T) {
	pcm := []int16{0, 1, -1, math.MaxInt16, math.MinInt16}
	assert.Equal(t, pcm, BytesToPCM16(PCM16ToBytes(pcm)))
	assert.Len(t, BytesToPCM16([]byte{1, 2, 3}), 1, "odd trailing byte is dropped")
}

func TestInterleave_PadsShorterTrack(t *testing.T) {
	assert.Equal(t, []int16{1, 10, 2, 0}, Interleave([]int16{1, 2}, []int16{10}))
}

func TestWAV_Header(t *testing.T) {
	wav := WAV([]int16{1, 2, 3, 4}, 16000, 2)
	require.Len(t, wav, 44+8)
	assert.Equal(t, "RIFF", string(wav[0:4]))
	assert.Equal(t, "WAVE", string(wav[8:12]))
	var hdr struct {
		Format, Channels    uint16
		Rate, ByteRate      uint32
		BlockAlign, BitsPer uint16
	}
	require.NoError(t, binary.Read(bytes.NewReader(wav[20:36]), binary.LittleEndian, &hdr))
	assert.Equal(t, uint16(1), hdr.Format)
	assert.Equal(t, uint16(2), hdr.Channels)
	assert.Equal(t, uint32(16000), hdr.Rate)
	assert.Equal(t, uint32(64000), hdr.ByteRate)
	assert.Equal(t, uint16(4), hdr.BlockAlign)
	assert.Equal(t, uint32(8), binary.LittleEndian.Uint32(wav[40:44]))
}

func TestOpus_RoundTrip(t *testing.T) {
	enc, err := NewOpusEncoder(32000)
	require.NoError(t, err)
	dec, err := NewOpusDecoder()
	require.NoError(t, err)

	src := sine(440, OpusRate, OpusRate/2, 8000)
	var out []int16
	for i := 0; i+OpusFrameSamples <= len(src); i += OpusFrameSamples {
		pkt, err := enc.Encode(src[i : i+OpusFrameSamples])
		require.NoError(t, err)
		assert.Less(t, len(pkt), 200, "32 kbps frames are small")
		pcm, err := dec.Decode(pkt)
		require.NoError(t, err)
		require.Len(t, pcm, OpusFrameSamples)
		out = append(out, pcm...)
	}
	assert.InDelta(t, RMS(src), RMS(out[OpusFrameSamples*5:]), 800, "tone level survives the codec")

	_, err = enc.Encode(make([]int16, 10))
	assert.Error(t, err, "only whole 20 ms frames are accepted")
}
