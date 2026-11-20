package social

// Testes de ponta a ponta da lógica social: vários Services ligados por um "servidor
// de sinalização" falso, em memória. Cobre amizade e salas sem rede nem janelas.

import (
	"crypto/ed25519"
	"encoding/base64"
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

// disconnect tira code do servidor (a pessoa fechou o app).
func (b *bus) disconnect(code string) {
	b.mu.Lock()
	delete(b.peers, code)
	b.mu.Unlock()
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

// endpoint implementa Net para um participante. key é a chave pública com que as
// mensagens dele chegam assinadas ("" = versão antiga, sem assinatura); a assinatura em
// si é testada no pacote signaling.
type endpoint struct {
	bus *bus
	me  string
	key string
}

func (e *endpoint) deliver(dst string, m signaling.Message) {
	m.Key = e.key
	e.bus.deliver(e.me, dst, m)
}

func (e *endpoint) Me() string     { return e.me }
func (e *endpoint) Status() string { return "" }
func (e *endpoint) Send(dst, t string, d any) error {
	p := signaling.Payload{T: t}
	if d != nil {
		p.D, _ = json.Marshal(d)
	}
	e.deliver(dst, signaling.Message{Kind: "OFFER", P: p})
	return nil
}
func (e *endpoint) SendOffer(dst, sdp string) error {
	e.deliver(dst, signaling.Message{Kind: "OFFER", P: signaling.Payload{T: "offer", SDP: &signaling.SDP{Type: "offer", SDP: sdp}}})
	return nil
}
func (e *endpoint) SendAnswer(dst, sdp string) error {
	e.deliver(dst, signaling.Message{Kind: "ANSWER", P: signaling.Payload{SDP: &signaling.SDP{Type: "answer", SDP: sdp}}})
	return nil
}
func (e *endpoint) SendCandidate(dst, owner string, c webrtc.ICECandidateInit) error {
	e.deliver(dst, signaling.Message{Kind: "CANDIDATE", P: signaling.Payload{Candidate: &c, ConnectionID: "mc_" + owner}})
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
	ui   *uiEvents
}

// uiEvents guarda o que o Service mandou para a página (toUI).
type uiEvents struct {
	mu   sync.Mutex
	list []map[string]any
}

func (u *uiEvents) add(ev map[string]any) {
	u.mu.Lock()
	u.list = append(u.list, ev)
	u.mu.Unlock()
}

// count conta os eventos de um tipo vindos de src.
func (u *uiEvents) count(kind, src string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, ev := range u.list {
		if ev["kind"] == kind && ev["src"] == src {
			n++
		}
	}
	return n
}

func join(b *bus, name, code string) *person {
	cfg := config.Config{Name: name, MyCode: code}
	cfg.Normalize()
	return joinWith(b, cfg, pubKey(cfg))
}

// joinOld entra como uma versão até a 0.6.1, que não assina as mensagens.
func joinOld(b *bus, name, code string) *person {
	cfg := config.Config{Name: name, MyCode: code}
	cfg.Normalize()
	return joinWith(b, cfg, "")
}

func joinWith(b *bus, cfg config.Config, key string) *person {
	h := &fakeHost{}
	ui := &uiEvents{}
	svc := New(cfg, func(config.Config) {}, h, ui.add)
	e := b.connect(cfg.MyCode, svc)
	e.key = key
	svc.Run(e)
	return &person{svc, cfg.MyCode, h, ui}
}

func pubKey(c config.Config) string {
	return base64.StdEncoding.EncodeToString(c.PrivateKey().Public().(ed25519.PublicKey))
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
	isFriend := func(p *person, code string) bool {
		for _, f := range p.State().Friends {
			if f.Code == code {
				return true
			}
		}
		return false
	}
	eventually(t, "amizade dos dois lados", func() bool { return isFriend(a, b.code) && isFriend(b, a.code) })
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

// Caio é amigo do Beto, que está na sala pública da Ana; Caio não é amigo da Ana.
func askSetup(t *testing.T) (ana, beto, caio *person) {
	t.Helper()
	b := newBus()
	ana, beto, caio = join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2"), join(b, "Caio", "caioooo3")
	befriend(t, ana, beto)
	befriend(t, beto, caio)
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "Beto entra na sala da Ana", func() bool { return roomMembers(beto) == 2 })
	caio.Join(ana.code)
	eventually(t, "Ana recebe o pedido do Caio", func() bool { return len(ana.State().Asks) == 1 })
	eventually(t, "Caio fica aguardando", func() bool { return caio.State().Asking })
	if roomMembers(caio) != 0 || roomMembers(ana) != 2 {
		t.Fatal("pedir para entrar não pode colocar na sala antes de o dono aceitar")
	}
	return
}

func TestAskToJoinAccepted(t *testing.T) {
	ana, _, caio := askSetup(t)
	if a := ana.State().Asks[0]; a.Code != caio.code || a.Name != "Caio" {
		t.Errorf("pedido errado: %+v", a)
	}
	ana.AnswerAsk(caio.code, true)
	eventually(t, "Caio entra depois de aceito", func() bool { return roomMembers(caio) == 3 && roomMembers(ana) == 3 })
	if s := caio.State(); s.Asking || s.Joining != "" || len(ana.State().Asks) != 0 {
		t.Error("o pedido deveria ter sido concluído")
	}
}

func TestAskToJoinRefused(t *testing.T) {
	ana, _, caio := askSetup(t)
	ana.AnswerAsk(caio.code, false)
	eventually(t, "Caio é avisado da recusa", func() bool {
		s := caio.State()
		return !s.Asking && s.Joining == ""
	})
	caio.Join(ana.code) // recusado não pede de novo nesta sala
	eventually(t, "segundo pedido é recusado direto", func() bool { return caio.State().Joining == "" })
	if len(ana.State().Asks) != 0 || roomMembers(caio) != 0 {
		t.Error("pedido recusado não pode voltar a aparecer")
	}
}

func TestAskToJoinCancelled(t *testing.T) {
	ana, _, caio := askSetup(t)
	caio.CancelJoin()
	eventually(t, "o pedido some para a Ana", func() bool { return len(ana.State().Asks) == 0 })
	if caio.State().Joining != "" {
		t.Error("Caio não deveria continuar pedindo")
	}
}

func TestCameraFlagAndSignaling(t *testing.T) {
	b := newBus()
	ana, beto, caio := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2"), join(b, "Caio", "caioooo3")
	befriend(t, ana, beto)
	befriend(t, ana, caio)
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "Beto entra", func() bool { return roomMembers(beto) == 2 })
	camOf := func(p *person, code string) bool {
		if r := p.State().Room; r != nil {
			if m := r.member(code); m != nil {
				return m.Camera
			}
		}
		return false
	}

	beto.SetCamera(true) // membro avisa o dono, que repassa à sala
	eventually(t, "a sala vê a câmera do Beto", func() bool { return camOf(ana, beto.code) && camOf(beto, beto.code) })
	ana.SetCamera(true) // dono
	eventually(t, "a sala vê a câmera da Ana", func() bool { return camOf(beto, ana.code) })
	beto.SetCamera(false)
	eventually(t, "câmera do Beto desligada", func() bool { return !camOf(ana, beto.code) })

	// a sinalização da câmera só passa entre quem está na mesma sala
	ana.CamSignal(beto.code, "cam-watch", nil)
	eventually(t, "Beto recebe o pedido da Ana", func() bool { return beto.ui.count("cam-watch", ana.code) == 1 })
	beto.CamSignal(ana.code, "cam-sdp", json.RawMessage(`{"who":"betoooo2","type":"offer","sdp":"x"}`))
	eventually(t, "Ana recebe a oferta", func() bool { return ana.ui.count("cam-sdp", beto.code) == 1 })
	caio.CamSignal(ana.code, "cam-watch", nil) // Caio não está em sala nenhuma: nem envia
	caio.net.Send(ana.code, "cam-watch", nil)  // e mesmo forçando, a Ana ignora
	ana.CamSignal(caio.code, "cam-watch", nil)
	ana.CamSignal(beto.code, "ping", nil) // só mensagens da câmera
	time.Sleep(100 * time.Millisecond)
	if ana.ui.count("cam-watch", caio.code) != 0 || caio.ui.count("cam-watch", ana.code) != 0 {
		t.Error("quem não está na sala não pode pedir nem receber câmera")
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

// Ver a própria tela: o pedido vai direto para o meu transmissor (não pela rede), e a
// oferta dele volta direto para a minha página.
func TestSelfViewStaysLocal(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	ana.SignalFromUI(ana.code, "watch", "", nil)
	eventually(t, "transmissor recebe o pedido da própria página", func() bool {
		ana.host.mu.Lock()
		defer ana.host.mu.Unlock()
		return len(ana.host.watched) == 1 && ana.host.watched[0] == ana.code
	})
	v := selfView{ana.Service}
	v.SendOffer(ana.code, "sdp-local")
	v.SendCandidate(ana.code, ana.code, webrtc.ICECandidateInit{Candidate: "c"})
	v.Send(ana.code, "bye", nil)
	if ana.ui.count("offer", ana.code) != 1 || ana.ui.count("cand", ana.code) != 1 || ana.ui.count("bye", ana.code) != 1 {
		t.Fatal("oferta, candidato e bye para mim mesmo deveriam ir direto para a página")
	}
	v.SendOffer(beto.code, "sdp-beto") // para os outros continua indo pela rede
	eventually(t, "Beto recebe a oferta", func() bool { return beto.ui.count("offer", ana.code) == 1 })
}

/* ---------- identidade (chave de cada amigo) ---------- */

func friendKey(p *person, code string) string {
	for _, f := range p.State().Config.Friends {
		if f.Code == code {
			return f.Key
		}
	}
	return ""
}

func hasNotice(p *person, part string) func() bool {
	var seen []string
	return func() bool {
		seen = append(seen, p.State().Notices...)
		for _, n := range seen {
			if strings.Contains(n, part) {
				return true
			}
		}
		return false
	}
}

func TestFriendKeysArePinned(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	eventually(t, "os dois guardam a chave um do outro", func() bool {
		return friendKey(ana, beto.code) == pubKey(beto.cfg) && friendKey(beto, ana.code) == pubKey(ana.cfg)
	})
}

func TestImpostorWithFriendCodeIsIgnored(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	eventually(t, "Ana guarda a chave do Beto", func() bool { return friendKey(ana, beto.code) != "" })
	ana.CreateRoom(true)

	// Beto fecha o app; alguém pega o código dele no servidor, com outra chave
	b.disconnect(beto.code)
	fake := config.Config{Name: "Beto", MyCode: beto.code}
	fake.Normalize()
	impostor := joinWith(b, fake, pubKey(fake))

	impostor.Join(ana.code)                    // sala pública: amigo entra direto
	impostor.net.Send(ana.code, "fr-del", nil) // e tenta desfazer a amizade
	impostor.net.Send(ana.code, "watch", nil)
	eventually(t, "Ana é avisada", hasNotice(ana, "se passar por Beto"))
	time.Sleep(50 * time.Millisecond)
	if roomMembers(ana) != 1 {
		t.Error("o impostor não pode entrar na sala da Ana")
	}
	if len(ana.State().Friends) != 1 {
		t.Error("o impostor não pode desfazer a amizade")
	}
	if len(ana.host.watched) != 0 {
		t.Error("o impostor não pode assistir")
	}

	// o Beto de verdade volta e tudo funciona
	b.disconnect(beto.code)
	beto2 := joinWith(b, beto.cfg, pubKey(beto.cfg))
	beto2.Join(ana.code)
	eventually(t, "Beto de verdade entra na sala", func() bool { return roomMembers(ana) == 2 })
}

func TestOldVersionFriendWorksUntilItSigns(t *testing.T) {
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), joinOld(b, "Beto", "betoooo2")
	befriend(t, ana, beto) // Beto ainda está na 0.6.1
	if friendKey(ana, beto.code) != "" {
		t.Fatal("sem assinatura, não há chave para guardar")
	}
	ana.CreateRoom(true)
	beto.Join(ana.code)
	eventually(t, "amigo em versão antiga continua entrando", func() bool { return roomMembers(ana) == 2 && roomMembers(beto) == 2 })
	beto.Leave()
	eventually(t, "Beto sai", func() bool { return roomMembers(ana) == 1 })

	// Beto atualiza: a primeira mensagem assinada fixa a chave
	b.disconnect(beto.code)
	beto2 := joinWith(b, beto.cfg, pubKey(beto.cfg))
	beto2.pingAll()
	eventually(t, "Ana guarda a chave nova", func() bool { return friendKey(ana, beto.code) == pubKey(beto.cfg) })

	// depois disso, mensagem sem assinatura com o código do Beto não vale mais
	b.disconnect(beto.code)
	old := joinWith(b, beto.cfg, "")
	old.Join(ana.code)
	eventually(t, "Ana é avisada", hasNotice(ana, "se passar por Beto"))
	if roomMembers(ana) != 1 {
		t.Error("depois de guardar a chave, mensagem sem assinatura é recusada")
	}
}

func TestReAddingFriendAcceptsNewKey(t *testing.T) {
	// o amigo perdeu a config e gerou chave nova: desfazer e refazer a amizade resolve
	b := newBus()
	ana, beto := join(b, "Ana", "anaaaaaa"), join(b, "Beto", "betoooo2")
	befriend(t, ana, beto)
	eventually(t, "chave guardada", func() bool { return friendKey(ana, beto.code) != "" })
	ana.RemoveFriend(beto.code)
	eventually(t, "amizade desfeita", func() bool { return len(ana.State().Friends) == 0 })

	b.disconnect(beto.code)
	nova := config.Config{Name: "Beto", MyCode: beto.code}
	nova.Normalize()
	beto2 := joinWith(b, nova, pubKey(nova))
	befriend(t, ana, beto2)
	eventually(t, "chave nova guardada", func() bool { return friendKey(ana, beto.code) == pubKey(nova) })
}

func TestPrivateKeyNeverReachesThePage(t *testing.T) {
	ana := join(newBus(), "Ana", "anaaaaaa")
	if ana.cfg.Key == "" {
		t.Fatal("a config deveria ter chave")
	}
	if k := ana.State().Config.Key; k != "" {
		t.Errorf("State (vai para a página) não pode levar a chave privada: %q", k)
	}
}

func TestVolumeIsSavedAndSafeToReadConcurrently(t *testing.T) {
	var mu sync.Mutex
	var saved config.Config
	cfg := config.Config{Name: "Ana", MyCode: "anaaaaaa"}
	cfg.Normalize()
	svc := New(cfg, func(c config.Config) { mu.Lock(); saved = c; mu.Unlock() }, &fakeHost{}, func(map[string]any) {})
	svc.Run(newBus().connect(cfg.MyCode, svc))

	// a página lendo o estado enquanto o volume muda (go test -race pega a corrida)
	stop := make(chan bool)
	done := make(chan bool)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- true
				return
			default:
				svc.SetVolume("betoooo2", i%100)
			}
		}
	}()
	for range 300 {
		json.Marshal(svc.State())
	}
	close(stop)
	<-done
	svc.SetVolume("betoooo2", 40)
	mu.Lock()
	defer mu.Unlock()
	if saved.Volumes["betoooo2"] != 40 {
		t.Errorf("volume deveria ir para o config salvo: %v", saved.Volumes)
	}
}
