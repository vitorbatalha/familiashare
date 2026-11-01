package signaling

import (
	"encoding/json"
	"testing"

	"github.com/pion/webrtc/v4"
)

// O servidor público do PeerJS derruba a conexão de quem envia mensagens fora do
// formato da biblioteca PeerJS. Estes testes garantem que tudo que o app envia
// imita esse formato (ver docs/PROTOCOL.md).

func decode(t *testing.T, m wireMsg) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		t.Fatalf("payload não é JSON: %v", err)
	}
	return p
}

func requireMediaShape(t *testing.T, m wireMsg, wantKind string) map[string]any {
	t.Helper()
	if m.Type != wantKind {
		t.Fatalf("type = %q, quero %q", m.Type, wantKind)
	}
	if m.Dst != Prefix+"bbbbbbbb" {
		t.Fatalf("dst = %q, quero o código com o prefixo %q", m.Dst, Prefix)
	}
	p := decode(t, m)
	if p["type"] != "media" {
		t.Errorf(`payload.type = %v, o servidor exige "media"`, p["type"])
	}
	if cid, _ := p["connectionId"].(string); cid == "" {
		t.Errorf("payload sem connectionId (o servidor exige)")
	}
	return p
}

func requireSDP(t *testing.T, p map[string]any, wantType string) {
	t.Helper()
	sdp, ok := p["sdp"].(map[string]any)
	if !ok {
		t.Fatalf("payload sem sdp (o servidor exige em OFFER/ANSWER): %v", p)
	}
	if sdp["type"] != wantType || sdp["sdp"] == "" {
		t.Errorf("sdp = %v, quero type %q e sdp preenchido", sdp, wantType)
	}
}

func TestControlMessageLooksLikePeerJSOffer(t *testing.T) {
	m := build("aaaaaaaa", "OFFER", "bbbbbbbb", controlPayload("room", map[string]any{"members": []string{"x"}}))
	p := requireMediaShape(t, m, "OFFER")
	requireSDP(t, p, "offer")
	if p["t"] != "room" {
		t.Errorf("t = %v, quero room", p["t"])
	}
	if _, ok := p["d"].(map[string]any); !ok {
		t.Errorf("d deveria carregar os dados como objeto: %v", p["d"])
	}
}

func TestControlMessageWithoutData(t *testing.T) {
	p := requireMediaShape(t, build("aaaaaaaa", "OFFER", "bbbbbbbb", controlPayload("bye", nil)), "OFFER")
	requireSDP(t, p, "offer")
	if _, has := p["d"]; has {
		t.Errorf("mensagem sem dados não deveria ter d: %v", p)
	}
}

func TestAnswerAndCandidateShape(t *testing.T) {
	ans := build("aaaaaaaa", "ANSWER", "bbbbbbbb", Payload{SDP: &SDP{"answer", "v=0\r\n"}, ConnectionID: "mc_bbbbbbbb"})
	requireSDP(t, requireMediaShape(t, ans, "ANSWER"), "answer")

	c := webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 1 1.2.3.4 5 typ host"}
	cand := build("aaaaaaaa", "CANDIDATE", "bbbbbbbb", Payload{Candidate: &c, ConnectionID: "mc_aaaaaaaa"})
	p := requireMediaShape(t, cand, "CANDIDATE")
	if _, ok := p["candidate"].(map[string]any); !ok {
		t.Errorf("CANDIDATE sem candidate: %v", p)
	}
}

func TestConnectionIDDefaultsToSender(t *testing.T) {
	p := decode(t, build("aaaaaaaa", "OFFER", "bbbbbbbb", controlPayload("ping", nil)))
	if p["connectionId"] != "mc_aaaaaaaa" {
		t.Errorf("connectionId = %v, quero mc_ + código de quem envia", p["connectionId"])
	}
}
