package realtime

import (
	"fmt"

	"github.com/thesyncim/gopus"
)

// WhatsApp calling media is mono Opus at 48 kHz in 20 ms frames.
const (
	OpusRate         = 48000
	OpusFrameSamples = 960
	opusMaxSamples   = 5760 // 120 ms, the largest Opus packet duration
	opusMaxPacket    = 1500
)

// OpusDecoder turns caller Opus packets into 48 kHz mono PCM16.
type OpusDecoder struct {
	dec *gopus.Decoder
	out []int16
}

func NewOpusDecoder() (*OpusDecoder, error) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(OpusRate, 1))
	if err != nil {
		return nil, fmt.Errorf("opus decoder: %w", err)
	}
	return &OpusDecoder{dec: dec, out: make([]int16, opusMaxSamples)}, nil
}

// Decode returns the samples for one packet. The slice is reused by the next call.
func (d *OpusDecoder) Decode(packet []byte) ([]int16, error) {
	n, err := d.dec.DecodeInt16(packet, d.out)
	if err != nil {
		return nil, err
	}
	return d.out[:n], nil
}

// OpusEncoder turns 20 ms frames of 48 kHz mono PCM16 into Opus packets.
type OpusEncoder struct {
	enc *gopus.Encoder
	buf []byte
}

func NewOpusEncoder(bitrate int) (*OpusEncoder, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: OpusRate, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		return nil, fmt.Errorf("opus encoder: %w", err)
	}
	if bitrate > 0 {
		if err := enc.SetBitrate(bitrate); err != nil {
			return nil, fmt.Errorf("opus bitrate: %w", err)
		}
	}
	return &OpusEncoder{enc: enc, buf: make([]byte, opusMaxPacket)}, nil
}

// Encode returns a new packet for exactly OpusFrameSamples samples.
func (e *OpusEncoder) Encode(frame []int16) ([]byte, error) {
	if len(frame) != OpusFrameSamples {
		return nil, fmt.Errorf("opus encode: want %d samples, got %d", OpusFrameSamples, len(frame))
	}
	n, err := e.enc.EncodeInt16(frame, e.buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), e.buf[:n]...), nil
}
