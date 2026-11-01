package main

// Sinalização via servidor público do PeerJS: cada pessoa fica conectada com um ID
// permanente (o código de amigo) enquanto o app está aberto. Por ele passam presença,
// pedidos de amizade, salas e a negociação WebRTC; o vídeo vai direto entre os PCs.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

const (
	peerServer = "wss://0.peerjs.com/peerjs"
	peerPrefix = "telau-"
)

var errIDTaken = errors.New("código em uso")

type sigMsg struct {
	Type    string          `json:"type"`
	Src     string          `json:"src,omitempty"`
	Dst     string          `json:"dst,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// O servidor público do PeerJS valida o formato e DERRUBA a conexão de quem envia algo
// diferente do que a biblioteca PeerJS envia. Por isso toda mensagem imita uma
// "media connection" do PeerJS (sdp + type + connectionId); o nosso dado vai em t/d.
type sdpField struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type sigPayload struct {
	SDP          *sdpField                `json:"sdp,omitempty"`
	Candidate    *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Type         string                   `json:"type"`
	ConnectionID string                   `json:"connectionId"`
	T            string                   `json:"t,omitempty"`
	D            json.RawMessage          `json:"d,omitempty"`
}

const emptySDP = "v=0\r\n"

func randString(n int, alphabet string) string {
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func newCode() string { return randString(8, "abcdefghjkmnpqrstuvwxyz23456789") }

func normCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

/* ---------- Uma conexão WebSocket com o servidor ---------- */

type signaler struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	done chan struct{}
}

func dialSignal(id string) (*signaler, error) {
	url := fmt.Sprintf("%s?key=peerjs&id=%s&token=%s&version=1.5.4", peerServer, id, randString(10, "abcdefghijklmnopqrstuvwxyz0123456789"))
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first sigMsg
	if err := ws.ReadJSON(&first); err != nil {
		ws.Close()
		return nil, err
	}
	switch first.Type {
	case "OPEN":
	case "ID-TAKEN":
		ws.Close()
		return nil, errIDTaken
	default:
		ws.Close()
		return nil, fmt.Errorf("servidor de sinalização: %s %s", first.Type, first.Payload)
	}
	ws.SetReadDeadline(time.Time{})
	s := &signaler{ws: ws, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				s.write(sigMsg{Type: "HEARTBEAT"})
			}
		}
	}()
	return s, nil
}

func (s *signaler) listen(onMsg func(sigMsg)) {
	go func() {
		defer s.Close()
		for {
			var m sigMsg
			if err := s.ws.ReadJSON(&m); err != nil {
				return
			}
			onMsg(m)
		}
	}()
}

func (s *signaler) write(m sigMsg) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.ws.WriteJSON(m)
}

func (s *signaler) Close() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
		s.ws.Close()
	}
}

/* ---------- Nó: identidade permanente, reconecta sozinho ---------- */

// inMsg é uma mensagem recebida, já com o código (sem prefixo) de quem enviou.
type inMsg struct {
	Kind string // OFFER | ANSWER | CANDIDATE | EXPIRE
	Src  string
	P    sigPayload
}

type node struct {
	me     string
	onMsg  func(inMsg)
	onOpen func()
	mu     sync.Mutex
	sig    *signaler
	status string
}

func startNode(me string, onMsg func(inMsg), onOpen func()) *node {
	n := &node{me: me, onMsg: onMsg, onOpen: onOpen, status: "conectando…"}
	go n.loop()
	return n
}

func (n *node) loop() {
	for {
		sig, err := dialSignal(peerPrefix + n.me)
		if err != nil {
			st := "sem conexão com o servidor · tentando…"
			if errors.Is(err, errIDTaken) {
				st = "seu código já está aberto em outro lugar (ou o app acabou de fechar) · tentando…"
			}
			log.Printf("sinalização: %v", err)
			n.setStatus(st)
			time.Sleep(4 * time.Second)
			continue
		}
		n.mu.Lock()
		n.sig, n.status = sig, ""
		n.mu.Unlock()
		sig.listen(n.dispatch)
		n.onOpen()
		<-sig.done
		n.mu.Lock()
		n.sig = nil
		n.mu.Unlock()
		n.setStatus("reconectando…")
		time.Sleep(time.Second)
	}
}

func (n *node) setStatus(s string) {
	n.mu.Lock()
	n.status = s
	n.mu.Unlock()
}

func (n *node) Status() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

func (n *node) dispatch(m sigMsg) {
	if !strings.HasPrefix(m.Src, peerPrefix) {
		return
	}
	in := inMsg{Kind: m.Type, Src: strings.TrimPrefix(m.Src, peerPrefix)}
	if len(m.Payload) > 0 {
		json.Unmarshal(m.Payload, &in.P)
	}
	n.onMsg(in)
}

func (n *node) send(kind, dst string, p sigPayload) error {
	n.mu.Lock()
	sig := n.sig
	n.mu.Unlock()
	if sig == nil {
		return errors.New("sem conexão")
	}
	p.Type = "media"
	if p.ConnectionID == "" {
		p.ConnectionID = "mc_" + n.me
	}
	b, _ := json.Marshal(p)
	return sig.write(sigMsg{Type: kind, Dst: peerPrefix + dst, Payload: b})
}

// Send manda uma mensagem de controle (t) com dados opcionais (d).
func (n *node) Send(dst, t string, d any) error {
	p := sigPayload{SDP: &sdpField{"offer", emptySDP}, T: t}
	if d != nil {
		p.D, _ = json.Marshal(d)
	}
	return n.send("OFFER", dst, p)
}

func (n *node) SendOffer(dst, sdp string) error {
	return n.send("OFFER", dst, sigPayload{SDP: &sdpField{"offer", sdp}, T: "offer"})
}

func (n *node) SendAnswer(dst, sdp string) error {
	return n.send("ANSWER", dst, sigPayload{SDP: &sdpField{"answer", sdp}, ConnectionID: "mc_" + dst})
}

// SendCandidate: streamOwner identifica a qual conexão o candidato pertence
// (a transmissão de quem), já que duas pessoas podem assistir uma à outra.
func (n *node) SendCandidate(dst, streamOwner string, c webrtc.ICECandidateInit) error {
	return n.send("CANDIDATE", dst, sigPayload{Candidate: &c, ConnectionID: "mc_" + streamOwner})
}

// Me é o código de amigo desta identidade.
func (n *node) Me() string { return n.me }
