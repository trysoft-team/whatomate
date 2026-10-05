package tts

import (
	"encoding/binary"
	"testing"
)

func TestNormalizeGeminiVoice(t *testing.T) {
	if got := NormalizeGeminiVoice("puck"); got != "Puck" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeGeminiVoice("unknown"); got != "Puck" {
		t.Fatalf("fallback got %q", got)
	}
}

func TestPCM16ToWAV(t *testing.T) {
	pcm := make([]byte, 100)
	wav := pcm16ToWAV(pcm, 24000)
	if len(wav) != 144 {
		t.Fatalf("wav len %d", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("bad header")
	}
	if binary.LittleEndian.Uint32(wav[24:28]) != 24000 {
		t.Fatalf("bad sample rate")
	}
}
