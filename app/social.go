package main

// Amigos, presença e salas — sem servidor próprio.
//
// Cada pessoa fica conectada ao servidor de sinalização com o seu código de amigo.
//   - Amizade: A envia "fr-req" para o código de B; B aceita ("fr-acc"); os dois guardam.
//   - Presença: a cada 15 s cada um manda "ping" (com a sala em que está) aos amigos,
//     que respondem "pong". Mensagem para quem está offline volta como EXPIRE.
//   - Sala: o dono coordena. Recebe "join", decide (pública = amigos do dono entram;
//     particular = só convidados), e manda o estado ("room") a todos os membros.
//   - Transmissão: qualquer membro transmite; quem quer assistir manda "watch" direto
//     para quem transmite, e a negociação WebRTC acontece entre os dois.

import (
	"encoding/json"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	pingEvery     = 15 * time.Second
	onlineTimeout = 40 * time.Second
	roomEvery     = 10 * time.Second
	roomTimeout   = 35 * time.Second
	joinTimeout   = 12 * time.Second
)

type RoomInfo struct {
	Owner     string `json:"owner"`
	OwnerName string `json:"ownerName"`
	Public    bool   `json:"public"`
	Members   int    `json:"members"`
	Streaming int    `json:"streaming"`
}

type Member struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Streaming bool   `json:"streaming"`
}

type Room struct {
	Owner     string          `json:"owner"`
	OwnerName string          `json:"ownerName"`
	Public    bool            `json:"public"`
	Members   []Member        `json:"members"`
	invited   map[string]bool // só o dono usa
	lastHeard time.Time       // só membros usam (último "room" do dono)
}

func (r *Room) member(code string) *Member {
	for i := range r.Members {
		if r.Members[i].Code == code {
			return &r.Members[i]
		}
	}
	return nil
}

type App struct {
	mu       sync.Mutex
	cfg      Config
	net      *node
	host     *Host
	toUI     func(event map[string]any) // eventos de WebRTC para o espectador (JS)
	lastSeen map[string]time.Time       // amigo → última mensagem
	friendAt map[string]*RoomInfo       // amigo → sala em que está
	requests map[string]string          // pedidos recebidos: código → nome
	invites  map[string]string          // convites recebidos: dono → nome
	room     *Room
	joining  string
	joinAt   time.Time
	notices  []string
}

func newApp(cfg Config, toUI func(map[string]any)) *App {
	a := &App{
		cfg: cfg, host: newHost(), toUI: toUI,
		lastSeen: map[string]time.Time{}, friendAt: map[string]*RoomInfo{},
		requests: map[string]string{}, invites: map[string]string{},
	}
	a.net = startNode(cfg.MyCode, a.onMsg, a.onConnected)
	go a.tick()
	return a
}

func (a *App) notice(s string) { a.notices = append(a.notices, s) } // com a.mu travado

func (a *App) Notice(s string) {
	a.mu.Lock()
	a.notice(s)
	a.mu.Unlock()
}

func (a *App) save() { saveConfig(a.cfg) } // com a.mu travado

func (a *App) isFriend(code string) bool {
	return slices.ContainsFunc(a.cfg.Friends, func(f Friend) bool { return f.Code == code })
}

func (a *App) friendName(code string) string {
	for _, f := range a.cfg.Friends {
		if f.Code == code {
			return f.Name
		}
	}
	if a.room != nil {
		if m := a.room.member(code); m != nil {
			return m.Name
		}
	}
	return code
}

/* ---------- Ciclo: presença, pedidos pendentes, saúde da sala ---------- */

func (a *App) onConnected() { a.pingAll() }

func (a *App) tick() {
	lastPing, lastRoom := time.Now(), time.Now()
	for range time.Tick(time.Second) {
		a.mu.Lock()
		now := time.Now()
		if a.joining != "" && now.Sub(a.joinAt) > joinTimeout {
			a.notice("Não foi possível entrar na sala de " + a.friendName(a.joining) + ".")
			a.joining = ""
		}
		owner := a.room != nil && a.room.Owner == a.cfg.MyCode
		if a.room != nil && !owner && now.Sub(a.room.lastHeard) > roomTimeout {
			a.notice("A conexão com a sala de " + a.room.OwnerName + " caiu.")
			a.leaveLocked(false)
		}
		a.mu.Unlock()
		if now.Sub(lastPing) >= pingEvery {
			lastPing = now
			a.pingAll()
		}
		if owner && now.Sub(lastRoom) >= roomEvery {
			lastRoom = now
			a.broadcastRoom()
		}
	}
}

func (a *App) myRoomInfo() *RoomInfo { // com a.mu travado
	r := a.room
	if r == nil {
		return nil
	}
	info := &RoomInfo{Owner: r.Owner, OwnerName: r.OwnerName, Public: r.Public, Members: len(r.Members)}
	for _, m := range r.Members {
		if m.Streaming {
			info.Streaming++
		}
	}
	return info
}

type hello struct {
	Name string    `json:"name"`
	Room *RoomInfo `json:"room,omitempty"`
}

func (a *App) pingAll() {
	a.mu.Lock()
	h := hello{a.cfg.Name, a.myRoomInfo()}
	friends := slices.Clone(a.cfg.Friends)
	pending := slices.Clone(a.cfg.Pending)
	a.mu.Unlock()
	for _, f := range friends {
		a.net.Send(f.Code, "ping", h)
	}
	for _, code := range pending { // reenvia até a pessoa ficar online e aceitar
		a.net.Send(code, "fr-req", h)
	}
}

/* ---------- Mensagens recebidas ---------- */

func (a *App) onMsg(m inMsg) {
	src := m.Src
	switch m.Kind {
	case "EXPIRE": // src está offline
		a.mu.Lock()
		delete(a.lastSeen, src)
		delete(a.friendAt, src)
		var leaveRoom, removeMember bool
		if a.room != nil {
			if a.room.Owner == a.cfg.MyCode {
				removeMember = a.room.member(src) != nil
			} else {
				leaveRoom = a.room.Owner == src
			}
		}
		if leaveRoom {
			a.notice("A sala de " + a.room.OwnerName + " foi encerrada.")
			a.leaveLocked(false)
		}
		if removeMember {
			a.removeMemberLocked(src)
		}
		a.mu.Unlock()
		if removeMember {
			a.broadcastRoom()
		}
		a.toUI(map[string]any{"kind": "gone", "src": src})
		return
	case "ANSWER":
		if m.P.SDP != nil && a.canWatchMe(src) {
			a.host.HandleAnswer(src, m.P.SDP.SDP)
		}
		return
	case "CANDIDATE":
		if m.P.Candidate == nil {
			return
		}
		if m.P.ConnectionID == "mc_"+a.cfg.MyCode { // conexão da minha transmissão
			a.host.HandleCandidate(src, *m.P.Candidate)
		} else { // conexão em que eu assisto src
			a.toUI(map[string]any{"kind": "cand", "src": src, "candidate": m.P.Candidate})
		}
		return
	case "OFFER":
	default:
		return
	}

	switch m.P.T {
	case "offer":
		if m.P.SDP != nil {
			a.toUI(map[string]any{"kind": "offer", "src": src, "sdp": m.P.SDP.SDP})
		}
	case "bye":
		a.toUI(map[string]any{"kind": "bye", "src": src})
	case "watch":
		if a.canWatchMe(src) {
			a.host.HandleWatch(src)
		} else {
			a.net.Send(src, "bye", nil)
		}
	case "unwatch":
		a.host.HandleUnwatch(src)
	default:
		a.onControl(src, m.P.T, m.P.D)
	}
}

func (a *App) canWatchMe(src string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.room != nil && a.room.member(src) != nil
}

func (a *App) onControl(src, t string, d json.RawMessage) {
	var h hello
	json.Unmarshal(d, &h)
	a.mu.Lock()
	var reply func()
	var broadcast bool
	switch t {
	case "fr-req":
		switch {
		case a.isFriend(src): // já é amigo (ex.: a outra pessoa reinstalou)
			reply = a.sendLater(src, "fr-acc", hello{Name: a.cfg.Name})
		case slices.Contains(a.cfg.Pending, src): // os dois se adicionaram
			a.addFriendLocked(src, h.Name)
			reply = a.sendLater(src, "fr-acc", hello{Name: a.cfg.Name})
		default:
			if _, ok := a.requests[src]; !ok {
				a.notice(h.Name + " quer ser seu amigo.")
			}
			a.requests[src] = h.Name
		}
	case "fr-acc":
		if slices.Contains(a.cfg.Pending, src) || a.isFriend(src) {
			if !a.isFriend(src) {
				a.notice(h.Name + " aceitou seu pedido de amizade.")
			}
			a.addFriendLocked(src, h.Name)
			reply = a.sendLater(src, "ping", hello{a.cfg.Name, a.myRoomInfo()})
		}
	case "fr-del":
		a.removeFriendLocked(src)
	case "ping", "pong":
		if a.isFriend(src) {
			a.lastSeen[src] = time.Now()
			a.friendAt[src] = h.Room
			a.renameFriendLocked(src, h.Name)
			if t == "ping" {
				reply = a.sendLater(src, "pong", hello{a.cfg.Name, a.myRoomInfo()})
			}
		}
	case "invite":
		if a.isFriend(src) {
			if _, ok := a.invites[src]; !ok {
				a.notice(h.Name + " te convidou para a sala.")
			}
			a.invites[src] = h.Name
		}
	case "uninvite":
		delete(a.invites, src)
	case "join":
		r := a.room
		if r == nil || r.Owner != a.cfg.MyCode {
			reply = a.sendLater(src, "join-no", map[string]string{"reason": "Essa sala não existe mais."})
			break
		}
		if r.member(src) == nil {
			if !(r.invited[src] || (r.Public && a.isFriend(src))) {
				reply = a.sendLater(src, "join-no", map[string]string{"reason": "Essa sala é particular: peça um convite."})
				break
			}
			r.Members = append(r.Members, Member{Code: src, Name: h.Name})
			delete(r.invited, src)
		}
		broadcast = true
	case "join-no":
		if a.joining == src {
			var x struct{ Reason string }
			json.Unmarshal(d, &x)
			a.notice(x.Reason)
			a.joining = ""
		}
	case "room":
		var r Room
		json.Unmarshal(d, &r)
		if r.Owner != src || (a.joining != src && (a.room == nil || a.room.Owner != src)) {
			break // não pedi para entrar nessa sala
		}
		if r.member(a.cfg.MyCode) == nil {
			if a.room != nil && a.room.Owner == src {
				a.notice("Você saiu da sala de " + r.OwnerName + ".")
				a.leaveLocked(false)
			}
			break
		}
		if a.joining == src {
			delete(a.invites, src)
			a.joining = ""
		}
		r.lastHeard = time.Now()
		a.room = &r
		allowed := map[string]bool{}
		for _, m := range r.Members {
			allowed[m.Code] = true
		}
		go a.host.KeepOnly(allowed)
	case "leave":
		if a.room != nil && a.room.Owner == a.cfg.MyCode && a.room.member(src) != nil {
			a.removeMemberLocked(src)
			broadcast = true
		}
	case "closed":
		if a.room != nil && a.room.Owner == src {
			a.notice("A sala de " + a.room.OwnerName + " foi encerrada.")
			a.leaveLocked(false)
		}
	case "share":
		var x struct{ On bool }
		json.Unmarshal(d, &x)
		if a.room != nil && a.room.Owner == a.cfg.MyCode {
			if m := a.room.member(src); m != nil {
				m.Streaming = x.On
				broadcast = true
			}
		}
	}
	a.mu.Unlock()
	if reply != nil {
		reply()
	}
	if broadcast {
		a.broadcastRoom()
	}
}

// sendLater prepara um envio para depois de soltar a.mu.
func (a *App) sendLater(dst, t string, d any) func() {
	return func() { a.net.Send(dst, t, d) }
}

/* ---------- Amigos ---------- */

func (a *App) addFriendLocked(code, name string) {
	a.cfg.Pending = slices.DeleteFunc(a.cfg.Pending, func(c string) bool { return c == code })
	delete(a.requests, code)
	if !a.isFriend(code) {
		a.cfg.Friends = append(a.cfg.Friends, Friend{Code: code, Name: name})
	}
	a.renameFriendLocked(code, name)
	a.lastSeen[code] = time.Now()
	a.save()
}

func (a *App) renameFriendLocked(code, name string) {
	if name == "" {
		return
	}
	for i := range a.cfg.Friends {
		if a.cfg.Friends[i].Code == code && a.cfg.Friends[i].Name != name {
			a.cfg.Friends[i].Name = name
			a.save()
		}
	}
}

func (a *App) removeFriendLocked(code string) {
	a.cfg.Friends = slices.DeleteFunc(a.cfg.Friends, func(f Friend) bool { return f.Code == code })
	a.cfg.Pending = slices.DeleteFunc(a.cfg.Pending, func(c string) bool { return c == code })
	delete(a.lastSeen, code)
	delete(a.friendAt, code)
	delete(a.invites, code)
	a.save()
}

func (a *App) AddFriend(code string) string {
	code = normCode(code)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case len(code) < 6:
		return "Código inválido."
	case code == a.cfg.MyCode:
		return "Esse é o seu próprio código."
	case a.isFriend(code):
		return "Vocês já são amigos."
	}
	if name, ok := a.requests[code]; ok { // a pessoa já tinha me adicionado
		a.addFriendLocked(code, name)
		go a.net.Send(code, "fr-acc", hello{Name: a.cfg.Name})
		return ""
	}
	if !slices.Contains(a.cfg.Pending, code) {
		a.cfg.Pending = append(a.cfg.Pending, code)
		a.save()
	}
	go a.net.Send(code, "fr-req", hello{Name: a.cfg.Name})
	return ""
}

func (a *App) AnswerRequest(code string, accept bool) {
	a.mu.Lock()
	name, ok := a.requests[code]
	delete(a.requests, code)
	if ok && accept {
		a.addFriendLocked(code, name)
	}
	me := a.cfg.Name
	a.mu.Unlock()
	if ok && accept {
		a.net.Send(code, "fr-acc", hello{Name: me})
	}
}

func (a *App) RemoveFriend(code string) {
	a.mu.Lock()
	a.removeFriendLocked(code)
	a.mu.Unlock()
	a.net.Send(code, "fr-del", nil)
}

func (a *App) SetName(name string) {
	a.mu.Lock()
	if name != "" && len([]rune(name)) <= 32 {
		a.cfg.Name = name
		a.save()
	}
	a.mu.Unlock()
	a.pingAll()
}

/* ---------- Salas ---------- */

func (a *App) CreateRoom(public bool) {
	a.mu.Lock()
	if a.room != nil {
		a.leaveLocked(true)
	}
	a.cfg.RoomPublic = public
	a.save()
	a.room = &Room{Owner: a.cfg.MyCode, OwnerName: a.cfg.Name, Public: public,
		Members: []Member{{Code: a.cfg.MyCode, Name: a.cfg.Name}}, invited: map[string]bool{}}
	a.mu.Unlock()
	a.pingAll()
}

func (a *App) SetRoomPublic(public bool) {
	a.mu.Lock()
	ok := a.room != nil && a.room.Owner == a.cfg.MyCode
	if ok {
		a.room.Public = public
		a.cfg.RoomPublic = public
		a.save()
	}
	a.mu.Unlock()
	if ok {
		a.broadcastRoom()
		a.pingAll()
	}
}

func (a *App) Invite(code string) {
	a.mu.Lock()
	ok := a.room != nil && a.room.Owner == a.cfg.MyCode && a.isFriend(code)
	if ok {
		a.room.invited[code] = true
	}
	name := a.cfg.Name
	a.mu.Unlock()
	if ok {
		a.net.Send(code, "invite", hello{Name: name})
	}
}

func (a *App) DeclineInvite(owner string) {
	a.mu.Lock()
	delete(a.invites, owner)
	a.mu.Unlock()
}

func (a *App) Join(owner string) {
	a.mu.Lock()
	if a.room != nil {
		if a.room.Owner == owner {
			a.mu.Unlock()
			return
		}
		a.leaveLocked(true)
	}
	a.joining, a.joinAt = owner, time.Now()
	name := a.cfg.Name
	a.mu.Unlock()
	a.net.Send(owner, "join", hello{Name: name})
}

func (a *App) Leave() {
	a.mu.Lock()
	a.leaveLocked(true)
	a.mu.Unlock()
	a.pingAll()
}

// leaveLocked sai da sala atual (como dono, encerra a sala para todos).
func (a *App) leaveLocked(notify bool) {
	r := a.room
	if r == nil {
		return
	}
	a.room = nil
	go a.host.Stop()
	a.toUI(map[string]any{"kind": "left"})
	if !notify {
		return
	}
	if r.Owner == a.cfg.MyCode {
		for _, m := range r.Members {
			if m.Code != a.cfg.MyCode {
				go a.net.Send(m.Code, "closed", nil)
			}
		}
		for code := range r.invited {
			go a.net.Send(code, "uninvite", nil)
		}
	} else {
		go a.net.Send(r.Owner, "leave", nil)
	}
}

func (a *App) removeMemberLocked(code string) {
	a.room.Members = slices.DeleteFunc(a.room.Members, func(m Member) bool { return m.Code == code })
	go a.host.HandleUnwatch(code)
}

func (a *App) broadcastRoom() {
	a.mu.Lock()
	r := a.room
	if r == nil || r.Owner != a.cfg.MyCode {
		a.mu.Unlock()
		return
	}
	snapshot := Room{Owner: r.Owner, OwnerName: r.OwnerName, Public: r.Public, Members: slices.Clone(r.Members)}
	a.mu.Unlock()
	for _, m := range snapshot.Members {
		if m.Code != snapshot.Owner {
			a.net.Send(m.Code, "room", snapshot)
		}
	}
}

/* ---------- Transmitir na sala ---------- */

func (a *App) StartShare(cfg Config) error {
	a.mu.Lock()
	if a.room == nil {
		a.mu.Unlock()
		a.CreateRoom(cfg.RoomPublic)
		a.mu.Lock()
	}
	a.mergeShareSettings(cfg)
	cfg = a.cfg
	a.mu.Unlock()
	if err := a.host.Start(cfg, a.net); err != nil {
		return err
	}
	a.setStreaming(true)
	return nil
}

func (a *App) StopShare() {
	a.host.Stop()
	a.setStreaming(false)
}

func (a *App) setStreaming(on bool) {
	a.mu.Lock()
	r := a.room
	if r == nil {
		a.mu.Unlock()
		return
	}
	owner := r.Owner == a.cfg.MyCode
	if owner {
		if m := r.member(a.cfg.MyCode); m != nil {
			m.Streaming = on
		}
	}
	a.mu.Unlock()
	if owner {
		a.broadcastRoom()
	} else {
		a.net.Send(r.Owner, "share", map[string]bool{"on": on})
	}
	a.pingAll()
}

// UpdateShare salva as configurações de transmissão e aplica ao vivo.
func (a *App) UpdateShare(cfg Config) {
	a.mu.Lock()
	a.mergeShareSettings(cfg)
	cfg = a.cfg
	a.mu.Unlock()
	a.host.Update(cfg)
}

func (a *App) mergeShareSettings(c Config) { // com a.mu travado
	a.cfg.Monitor, a.cfg.Height, a.cfg.FPS, a.cfg.Mbps = c.Monitor, c.Height, c.FPS, c.Mbps
	a.cfg.Codec, a.cfg.Encoder, a.cfg.Audio, a.cfg.Exclude = c.Codec, c.Encoder, c.Audio, c.Exclude
	a.cfg.normalize()
	a.save()
}

// SignalFromUI: mensagens WebRTC do espectador (JS) para quem transmite.
func (a *App) SignalFromUI(dst, kind, sdp string, cand *webrtc.ICECandidateInit) {
	switch kind {
	case "watch", "unwatch":
		a.net.Send(dst, kind, nil)
	case "answer":
		a.net.SendAnswer(dst, sdp)
	case "cand":
		if cand != nil {
			a.net.SendCandidate(dst, dst, *cand)
		}
	}
}

/* ---------- Estado para a interface ---------- */

type friendView struct {
	Code   string    `json:"code"`
	Name   string    `json:"name"`
	Online bool      `json:"online"`
	Room   *RoomInfo `json:"room,omitempty"`
}

type State struct {
	Name     string            `json:"name"`
	Code     string            `json:"code"`
	Net      string            `json:"net"`
	Friends  []friendView      `json:"friends"`
	Requests []Friend          `json:"requests"`
	Pending  []string          `json:"pending"`
	Invites  []Friend          `json:"invites"`
	Room     *Room             `json:"room"`
	IsOwner  bool              `json:"isOwner"`
	Invited  []string          `json:"invited"`
	Joining  string            `json:"joining"`
	Host     Stats             `json:"host"`
	Notices  []string          `json:"notices"`
	Names    map[string]string `json:"names"`
	Config   Config            `json:"config"`
}

func (a *App) State() State {
	hostStats := a.host.Stats()
	a.mu.Lock()
	defer a.mu.Unlock()
	// listas sempre como [] (nunca null) para a interface
	s := State{Name: a.cfg.Name, Code: a.cfg.MyCode, Net: a.net.Status(), Pending: append([]string{}, a.cfg.Pending...),
		Joining: a.joining, Host: hostStats, Notices: a.notices, Names: map[string]string{}, Config: a.cfg,
		Friends: []friendView{}, Requests: []Friend{}, Invites: []Friend{}, Invited: []string{}}
	if s.Notices == nil {
		s.Notices = []string{}
	}
	a.notices = nil
	now := time.Now()
	for _, f := range a.cfg.Friends {
		online := now.Sub(a.lastSeen[f.Code]) < onlineTimeout
		fv := friendView{Code: f.Code, Name: f.Name, Online: online}
		if online {
			fv.Room = a.friendAt[f.Code]
		}
		s.Friends = append(s.Friends, fv)
		s.Names[f.Code] = f.Name
	}
	for code, name := range a.requests {
		s.Requests = append(s.Requests, Friend{code, name})
	}
	for owner, name := range a.invites {
		s.Invites = append(s.Invites, Friend{owner, name})
	}
	if a.room != nil {
		r := *a.room
		r.Members = append([]Member{}, r.Members...)
		s.Room = &r
		s.IsOwner = r.Owner == a.cfg.MyCode
		for code := range a.room.invited {
			s.Invited = append(s.Invited, code)
		}
		for _, m := range r.Members {
			s.Names[m.Code] = m.Name
		}
	}
	return s
}

func (a *App) Shutdown() {
	a.mu.Lock()
	a.leaveLocked(true)
	a.mu.Unlock()
	a.host.Stop()
	time.Sleep(300 * time.Millisecond) // dá tempo de avisar a sala
	log.Printf("encerrado")
}
