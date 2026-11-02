package social

// Testes de ponta a ponta da lógica social: vários Services ligados por um "servidor
// de sinalização" falso, em memória. Cobre amizade e salas sem rede nem janelas.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/vitorbatalha/familiashare/internal/config"
	"github.com/vitorbatalha/familiashare/internal/signaling"
	"github.com/vitorbatalha/familiashare/internal/stream"
)

/* ---------- servidor falso ---------- */

// bus entrega mensagens em ordem para cada destino, como o servidor PeerJS. Mensagem
// para quem não está conectado volta como EXPIRE para quem enviou.
type bus struct {
	mu    sync.Mutex
	peers map[string]chan signaling.Message
}

func newBus() *bus { return &bus{peers: map[string]chan signaling.Message{}} }

func (b *bus) connect(code string, svc *Service) *endpoint {
	ch := make(chan signaling.Message, 256)
	b.mu.Lock()
	b.peers[code] = ch
	b.mu.Unlock()
	go func() {
		for m := range ch {
			svc.OnMessage(m)
		}
	}()
	return &endpoint{bus: b, me: code}
}

func (b *bus) deliver(from, to string, m signaling.Message) {
	b.mu.Lock()
	ch, ok := b.peers[to]
	back := b.peers[from]
	b.mu.Unlock()
	if !ok {
		if back != nil {
			back <- signaling.Message{Kind: "EXPIRE", Src: to}
		}
		return
	}
	m.Src = from
	ch <- m
}

// endpoint implementa Net para um participante.
type endpoint struct {
	bus *bus
	me  string
}

func (e *endpoint) Me() string     { return e.me }
func (e *endpoint) Status() string { return "" }
func (e *endpoint) Send(dst, t string, d any) error {
	p := signaling.Payload{T: t}
	if d != nil {
		p.D, _ = json.Marshal(d)
	}
	e.bus.deliver(e.me, dst, signaling.Message{Kind: "OFFER", P: p})
	return nil
}
func (e *endpoint) SendOffer(dst, sdp string) error {
	e.bus.deliver(e.me, dst, signaling.Message{Kind: "OFFER", P: signaling.Payload{T: "offer", SDP: &signaling.SDP{Type: "offer", SDP: sdp}}})
	return nil
}
func (e *endpoint) SendAnswer(dst, sdp string) error {
	e.bus.deliver(e.me, dst, signaling.Message{Kind: "ANSWER", P: signaling.Payload{SDP: &signaling.SDP{Type: "answer", SDP: sdp}}})
	return nil
}
func (e *endpoint) SendCandidate(dst, owner string, c webrtc.ICECandidateInit) error {
	e.bus.deliver(e.me, dst, signaling.Message{Kind: "CANDIDATE", P: signaling.Payload{Candidate: &c, ConnectionID: "mc_" + owner}})
	return nil
}

/* ---------- transmissor falso ---------- */

type fakeHost struct {
	mu      sync.Mutex
	watched []string
}

func (h *fakeHost) Start(config.Config, stream.Signaler) error      { return nil }
func (h *fakeHost) Update(config.Config)                            {}
func (h *fakeHost) Stop()                                           {}
func (h *fakeHost) Stats() stream.Stats                             { return stream.Stats{State: "idle"} }
func (h *fakeHost) HandleUnwatch(string)                            {}
func (h *fakeHost) HandleAnswer(string, string)                     {}
func (h *fakeHost) HandleCandidate(string, webrtc.ICECandidateInit) {}
func (h *fakeHost) KeepOnly(map[string]bool)                        {}
func (h *fakeHost) HandleWatch(id string) {
	h.mu.Lock()
	h.watched = append(h.watched, id)
	h.mu.Unlock()
}

/* ---------- participantes ---------- */

type person struct {
	*Service
	code string
	host *fakeHost
}

func join(b *bus, name, code string) *person {
	cfg := config.Config{Name: name, MyCode: code}
	cfg.Normalize()
	h := &fakeHost{}
	svc := New(cfg, func(config.Config) {}, h, func(map[string]any) {})
	svc.Run(b.connect(code, svc))
	return &person{svc, code, h}
}

// eventually espera a condição ficar verdadeira (as mensagens são assíncronas).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("não aconteceu: %s", what)
}

func befriend(t *testing.T, a, b *person) {
	t.Helper()
	if msg := a.AddFriend(b.code); msg != "" {
		t.Fatalf("AddFriend: %s", msg)
	}
	eventually(t, b.code+" recebe o pedido", func() bool { return len(b.State().Requests) == 1 })
	b.AnswerRequest(a.code, true)
	eventually(t, "amizade dos dois lados", func() bool {
		return len(a.State().Friends) == 1 && len(b.State().Friends) == 1
	})
}

func roomMembers(p *person) int {
	if r := p.State().Room; r != nil {
		return len(r.Members)
	}
	return 0
}

/* ---------- testes ---------- */

func TestFriendRequestAndPresence(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	s := ana.State()
	if s.Friends[0].Name != "Beto" || !s.Friends[0].Online {
		t.Errorf("Ana deveria ver Beto online: %+v", s.Friends[0])
	}
	if len(s.Pending) != 0 {
		t.Errorf("pedido aceito não deveria continuar pendente: %v", s.Pending)
	}
}

func TestAddFriendValidation(t *testing.T) {
	ana := join(newBus(), "Ana", "anaaaaaa")
	if msg := ana.AddFriend("abc"); msg == "" {
		t.Error("código curto deveria ser recusado")
	}
	if msg := ana.AddFriend(" ANAAAAAA "); msg == "" {
		t.Error("o próprio código deveria ser recusado")
	}
}

func TestPublicRoomFriendsJoinWithoutInvite(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "Beto entra na sala pública", func() bool { return roomMembers(beto) == 2 && roomMembers(ana) == 2 })
	if !ana.State().IsOwner || beto.State().IsOwner {
		t.Error("só a Ana é dona")
	}
}

func TestPrivateRoomRequiresInvite(t *testing.T) {
	b := newBus()
	ana, caio := join(b, "Ana", "anaaaaaa"), join(b, "Caio", "caioooo3")
	befriend(t, ana, caio)
	ana.CreateRoom(false)

	caio.Join(ana.code)
	eventually(t, "Caio é recusado com aviso", func() bool {
		for _, n := range caio.State().Notices {
			if strings.Contains(n, "particular") {
				return true
			}
		}
		return false
	})
	if roomMembers(caio) != 0 {
		t.Fatal("sem convite, Caio não pode entrar")
	}

	ana.Invite(caio.code)
	eventually(t, "Caio recebe o convite", func() bool { return len(caio.State().Invites) == 1 })
	caio.Join(ana.code)
	eventually(t, "Caio entra com convite", func() bool { return roomMembers(caio) == 2 })
	if len(caio.State().Invites) != 0 {
		t.Error("o convite deveria sumir depois de usado")
	}
}

func TestPrivateRoomHiddenFromFriends(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	anaRoom := func() *RoomInfo {
		for _, f := range beto.State().Friends {
			if f.Code == ana.code {
				return f.Room
			}
		}
		return nil
	}
	ana.CreateRoom(true)
	ana.pingAll()
	eventually(t, "Beto vê a sala pública da Ana", func() bool { return anaRoom() != nil })

	ana.SetRoomPublic(false)
	ana.pingAll()
	eventually(t, "a sala some quando fica particular", func() bool { return anaRoom() == nil })
	if f := beto.State().Friends[0]; !f.Online {
		t.Error("Ana continua aparecendo online")
	}
}

func TestOwnerLeavingClosesRoom(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "Beto na sala", func() bool { return roomMembers(beto) == 2 })
	ana.Leave()
	eventually(t, "sala encerrada para o Beto", func() bool { return beto.State().Room == nil })
}

func TestOnlyMembersCanWatch(t *testing.T) {
	b := newBus()
	ana, beto, zeca := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2"), join(b, "Zeca", "zecaaaa4")
	befriend(t, ana, beto)
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "Beto na sala", func() bool { return roomMembers(beto) == 2 })

	beto.SignalFromUI(ana.code, "watch", "", nil)
	zeca.SignalFromUI(ana.code, "watch", "", nil) // não é membro
	eventually(t, "Ana recebe o pedido do Beto", func() bool {
		ana.host.mu.Lock()
		defer ana.host.mu.Unlock()
		return len(ana.host.watched) == 1
	})
	time.Sleep(50 * time.Millisecond)
	ana.host.mu.Lock()
	defer ana.host.mu.Unlock()
	if len(ana.host.watched) != 1 || ana.host.watched[0] != beto.code {
		t.Errorf("só membros podem assistir: %v", ana.host.watched)
	}
}
