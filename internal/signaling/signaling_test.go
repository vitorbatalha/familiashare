package signaling

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
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
	m := build("aaaaaaaa", nil, "OFFER", "bbbbbbbb", controlPayload("room", map[string]any{"members": []string{"x"}}))
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
	p := requireMediaShape(t, build("aaaaaaaa", nil, "OFFER", "bbbbbbbb", controlPayload("bye", nil)), "OFFER")
	requireSDP(t, p, "offer")
	if _, has := p["d"]; has {
		t.Errorf("mensagem sem dados não deveria ter d: %v", p)
	}
}

func TestAnswerAndCandidateShape(t *testing.T) {
	ans := build("aaaaaaaa", nil, "ANSWER", "bbbbbbbb", Payload{SDP: &SDP{"answer", "v=0\r\n"}, ConnectionID: "mc_bbbbbbbb"})
	requireSDP(t, requireMediaShape(t, ans, "ANSWER"), "answer")

	c := webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 1 1.2.3.4 5 typ host"}
	cand := build("aaaaaaaa", nil, "CANDIDATE", "bbbbbbbb", Payload{Candidate: &c, ConnectionID: "mc_aaaaaaaa"})
	p := requireMediaShape(t, cand, "CANDIDATE")
	if _, ok := p["candidate"].(map[string]any); !ok {
		t.Errorf("CANDIDATE sem candidate: %v", p)
	}
}

func TestConnectionIDDefaultsToSender(t *testing.T) {
	p := decode(t, build("aaaaaaaa", nil, "OFFER", "bbbbbbbb", controlPayload("ping", nil)))
	if p["connectionId"] != "mc_aaaaaaaa" {
		t.Errorf("connectionId = %v, quero mc_ + código de quem envia", p["connectionId"])
	}
}

/* ---------- Assinatura ---------- */

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pubOf(k ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
}

// viaServer imita o servidor PeerJS (Node.js): ele lê o JSON e escreve de novo, com
// outra ordem de campos e sem os escapes de HTML que o Go usa (< etc.).
func viaServer(t *testing.T, m wireMsg, src string) wireMsg {
	t.Helper()
	var v any
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return wireMsg{Type: m.Type, Src: Prefix + src, Payload: bytes.TrimSpace(buf.Bytes())}
}

// receive entrega a mensagem a um Node de verdade (o mesmo caminho do app) e devolve o
// que chegou ao social (nil = descartada).
func receive(t *testing.T, me string, m wireMsg) *Message {
	t.Helper()
	var got *Message
	n := &Node{me: me, onMsg: func(m Message) { got = &m }}
	n.dispatch(m)
	return got
}

func TestSignedMessagesSurviveServerRewrite(t *testing.T) {
	ana := testKey(t)
	c := webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 2122260223 192.168.0.2 50000 typ host"}
	msgs := []wireMsg{
		build("aaaaaaaa", ana, "OFFER", "bbbbbbbb", controlPayload("ping", map[string]any{"name": "Ana & João <3", "room": map[string]any{"public": true, "owner": "aaaaaaaa"}})),
		build("aaaaaaaa", ana, "OFFER", "bbbbbbbb", Payload{SDP: &SDP{"offer", "v=0\r\na=group:BUNDLE 0 1\r\n"}, T: "offer"}),
		build("aaaaaaaa", ana, "ANSWER", "bbbbbbbb", Payload{SDP: &SDP{"answer", "v=0\r\n"}, ConnectionID: "mc_bbbbbbbb"}),
		build("aaaaaaaa", ana, "CANDIDATE", "bbbbbbbb", Payload{Candidate: &c, ConnectionID: "mc_aaaaaaaa"}),
	}
	for _, m := range msgs {
		got := receive(t, "bbbbbbbb", viaServer(t, m, "aaaaaaaa"))
		if got == nil {
			t.Errorf("%s assinada foi descartada depois de passar pelo servidor: %s", m.Type, m.Payload)
			continue
		}
		if got.Key != pubOf(ana) {
			t.Errorf("%s: Key = %q, quero a chave pública da Ana", m.Type, got.Key)
		}
	}
}

func TestTamperedMessageIsDropped(t *testing.T) {
	m := viaServer(t, build("aaaaaaaa", testKey(t), "OFFER", "bbbbbbbb", controlPayload("join", map[string]string{"name": "Ana"})), "aaaaaaaa")
	var p map[string]any
	json.Unmarshal(m.Payload, &p)
	p["t"] = "fr-del" // alguém no caminho troca o tipo da mensagem
	m.Payload, _ = json.Marshal(p)
	if receive(t, "bbbbbbbb", m) != nil {
		t.Error("mensagem alterada deveria ser descartada")
	}
}

func TestSignatureIsBoundToSenderAndDestination(t *testing.T) {
	ana := testKey(t)
	m := build("aaaaaaaa", ana, "OFFER", "bbbbbbbb", controlPayload("join", nil))
	// quem recebeu a mensagem da Ana tenta reaproveitá-la com outra pessoa
	if receive(t, "cccccccc", viaServer(t, m, "aaaaaaaa")) != nil {
		t.Error("mensagem assinada para bbbbbbbb não pode valer para cccccccc")
	}
	// ou repassá-la com o próprio código
	if receive(t, "bbbbbbbb", viaServer(t, m, "dddddddd")) != nil {
		t.Error("mensagem assinada por aaaaaaaa não pode valer vinda de dddddddd")
	}
}

func TestImpostorSignsWithOwnKey(t *testing.T) {
	// Quem pegou o ID da Ana (offline) assina com a própria chave: a assinatura confere,
	// mas a chave não é a da Ana. Quem compara com a chave guardada é o social.
	ana, impostor := testKey(t), testKey(t)
	got := receive(t, "bbbbbbbb", viaServer(t, build("aaaaaaaa", impostor, "OFFER", "bbbbbbbb", controlPayload("ping", nil)), "aaaaaaaa"))
	if got == nil || got.Key != pubOf(impostor) || got.Key == pubOf(ana) {
		t.Errorf("deveria chegar com a chave do impostor, para o social recusar: %+v", got)
	}
}

func TestUnsignedMessagesStillArrive(t *testing.T) {
	// versões até a 0.6.1 não assinam: a mensagem chega com Key vazia
	got := receive(t, "bbbbbbbb", viaServer(t, build("aaaaaaaa", nil, "OFFER", "bbbbbbbb", controlPayload("ping", nil)), "aaaaaaaa"))
	if got == nil || got.Key != "" {
		t.Errorf("mensagem sem assinatura deveria chegar com Key vazia: %+v", got)
	}
}

func TestGarbageSignatureIsDropped(t *testing.T) {
	for _, p := range []Payload{
		{T: "ping", K: "não é base64", S: "x"},
		{T: "ping", K: pubOf(testKey(t))}, // chave sem assinatura
		{T: "ping", K: base64.StdEncoding.EncodeToString([]byte("curta")), S: "AAAA"},
	} {
		m := build("aaaaaaaa", nil, "OFFER", "bbbbbbbb", p)
		if receive(t, "bbbbbbbb", viaServer(t, m, "aaaaaaaa")) != nil {
			t.Errorf("assinatura inválida deveria ser descartada: %+v", p)
		}
	}
}

func TestExpireIsNotVerified(t *testing.T) {
	// EXPIRE é gerado pelo servidor (o destino está offline), nunca vem assinado
	if receive(t, "bbbbbbbb", wireMsg{Type: "EXPIRE", Src: Prefix + "aaaaaaaa"}) == nil {
		t.Error("EXPIRE deveria chegar")
	}
}
