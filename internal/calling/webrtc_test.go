package calling

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/trysoft/whatomate/internal/config"
)

func TestCreatePeerConnection_AnswersAsDTLSServer(t *testing.T) {
	m := &Manager{log: testLogger(), config: &config.CallingConfig{}}

	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = offerer.Close() }()
	if _, err := offerer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(offer.SDP, "a=setup:actpass") {
		t.Fatalf("offer should be actpass like WhatsApp's:\n%s", offer.SDP)
	}

	pc, err := m.createPeerConnection()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	if err := pc.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.SDP, "a=setup:passive") {
		t.Errorf("answer must be a=setup:passive so the remote starts DTLS after it has our answer:\n%s", answer.SDP)
	}
}
