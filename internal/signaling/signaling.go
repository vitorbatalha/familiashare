// Package signaling conecta o app ao servidor público de sinalização do PeerJS.
//
// Cada pessoa fica conectada com um ID permanente (o código de amigo, com o prefixo
// "telau-") enquanto o app está aberto. Por essa conexão passam presença, pedidos de
// amizade, salas e a negociação WebRTC; o vídeo vai direto entre os PCs.
// O formato das mensagens está documentado em docs/PROTOCOL.md.
package signaling

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
	server = "wss://0.peerjs.com/peerjs"
	// Prefix vai na frente do código de amigo para formar o ID no servidor.
	// Mantido desde a primeira versão ("tela"): mudar quebraria a comunicação
	// com quem ainda não atualizou.
	Prefix = "telau-"
)

var errIDTaken = errors.New("código em uso")

// wireMsg é o envelope do servidor PeerJS.
type wireMsg struct {
	Type    string          `json:"type"`
	Src     string          `json:"src,omitempty"`
	Dst     string          `json:"dst,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// O servidor público do PeerJS valida o formato e DERRUBA a conexão de quem envia algo
// diferente do que a biblioteca PeerJS envia. Por isso toda mensagem imita uma
// "media connection" do PeerJS (sdp + type + connectionId); o nosso dado vai em t/d.

// SDP é o campo sdp de uma oferta ou resposta.
type SDP struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// Payload é o conteúdo de uma mensagem.
type Payload struct {
	SDP          *SDP                     `json:"sdp,omitempty"`
	Candidate    *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Type         string                   `json:"type"`
	ConnectionID string                   `json:"connectionId"`
	T            string                   `json:"t,omitempty"` // tipo da nossa mensagem (ping, join, room...)
	D            json.RawMessage          `json:"d,omitempty"` // dados da nossa mensagem
}

const emptySDP = "v=0\r\n"

// Message é uma mensagem recebida, já com o código (sem prefixo) de quem enviou.
type Message struct {
	Kind string // OFFER | ANSWER | CANDIDATE | EXPIRE (EXPIRE = o destino estava offline)
	Src  string
	P    Payload
}

/* ---------- Uma conexão WebSocket com o servidor ---------- */

type conn struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	done chan struct{}
}

func randToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func dial(id string) (*conn, error) {
	url := fmt.Sprintf("%s?key=peerjs&id=%s&token=%s&version=1.5.4", server, id, randToken(10))
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first wireMsg
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
	c := &conn{ws: ws, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-t.C:
				c.write(wireMsg{Type: "HEARTBEAT"})
			}
		}
	}()
	return c, nil
}

func (c *conn) listen(onMsg func(wireMsg)) {
	go func() {
		defer c.close()
		for {
			var m wireMsg
			if err := c.ws.ReadJSON(&m); err != nil {
				return
			}
			onMsg(m)
		}
	}()
}

func (c *conn) write(m wireMsg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.ws.WriteJSON(m)
}

func (c *conn) close() {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
	default:
		close(c.done)
		c.ws.Close()
	}
}

/* ---------- Node: identidade permanente, reconecta sozinho ---------- */

// Node é a presença desta pessoa no servidor. Reconecta sozinho se a conexão cair.
type Node struct {
	me     string
	onMsg  func(Message)
	onOpen func()
	mu     sync.Mutex
	c      *conn
	status string
}

// Start conecta com o código de amigo me. onMsg recebe as mensagens em ordem
// (numa única goroutine); onOpen é chamado a cada (re)conexão.
func Start(me string, onMsg func(Message), onOpen func()) *Node {
	n := &Node{me: me, onMsg: onMsg, onOpen: onOpen, status: "conectando…"}
	go n.loop()
	return n
}

func (n *Node) loop() {
	for {
		c, err := dial(Prefix + n.me)
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
		n.c, n.status = c, ""
		n.mu.Unlock()
		c.listen(n.dispatch)
		n.onOpen()
		<-c.done
		n.mu.Lock()
		n.c = nil
		n.mu.Unlock()
		n.setStatus("reconectando…")
		time.Sleep(time.Second)
	}
}

func (n *Node) setStatus(s string) {
	n.mu.Lock()
	n.status = s
	n.mu.Unlock()
}

// Status descreve problemas de conexão ("" = conectado).
func (n *Node) Status() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

// Me é o código de amigo desta identidade.
func (n *Node) Me() string { return n.me }

func (n *Node) dispatch(m wireMsg) {
	if !strings.HasPrefix(m.Src, Prefix) {
		return
	}
	in := Message{Kind: m.Type, Src: strings.TrimPrefix(m.Src, Prefix)}
	if len(m.Payload) > 0 {
		json.Unmarshal(m.Payload, &in.P)
	}
	n.onMsg(in)
}

// build monta o envelope no formato que o servidor PeerJS aceita (ver PROTOCOL.md).
func build(me, kind, dst string, p Payload) wireMsg {
	p.Type = "media"
	if p.ConnectionID == "" {
		p.ConnectionID = "mc_" + me
	}
	b, _ := json.Marshal(p)
	return wireMsg{Type: kind, Dst: Prefix + dst, Payload: b}
}

func (n *Node) send(kind, dst string, p Payload) error {
	n.mu.Lock()
	c := n.c
	n.mu.Unlock()
	if c == nil {
		return errors.New("sem conexão")
	}
	return c.write(build(n.me, kind, dst, p))
}

// controlPayload é uma mensagem de controle: t (tipo) e d (dados opcionais).
func controlPayload(t string, d any) Payload {
	p := Payload{SDP: &SDP{"offer", emptySDP}, T: t}
	if d != nil {
		p.D, _ = json.Marshal(d)
	}
	return p
}

// Send manda uma mensagem de controle (t) com dados opcionais (d).
func (n *Node) Send(dst, t string, d any) error { return n.send("OFFER", dst, controlPayload(t, d)) }

// SendOffer manda a oferta WebRTC de uma transmissão para quem vai assistir.
func (n *Node) SendOffer(dst, sdp string) error {
	return n.send("OFFER", dst, Payload{SDP: &SDP{"offer", sdp}, T: "offer"})
}

// SendAnswer manda a resposta WebRTC de quem assiste para quem transmite.
func (n *Node) SendAnswer(dst, sdp string) error {
	return n.send("ANSWER", dst, Payload{SDP: &SDP{"answer", sdp}, ConnectionID: "mc_" + dst})
}

// SendCandidate manda um candidato ICE. streamOwner identifica a qual conexão ele
// pertence (a transmissão de quem), já que duas pessoas podem assistir uma à outra.
func (n *Node) SendCandidate(dst, streamOwner string, c webrtc.ICECandidateInit) error {
	return n.send("CANDIDATE", dst, Payload{Candidate: &c, ConnectionID: "mc_" + streamOwner})
}
