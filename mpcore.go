package main

// Crosstown multiplayer server: rooms ("EU Server #5d69"), unique player names, groups, invites,
// position sync and simple events (hits, chat). Plain HTTP + JSON so it works from any browser.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const mpPort = 7777
const roomMax = 20

var mpTimeout = func() time.Duration {
	if v := os.Getenv("CT_MPTIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 15 * time.Second
}()

type mpEvent struct {
	K    string          `json:"k"`
	From string          `json:"from,omitempty"`
	D    json.RawMessage `json:"d,omitempty"`
}
type mpPlayer struct {
	Name, Token, Room, Group string
	Last                     time.Time
	State                    json.RawMessage
	Events                   []mpEvent
	Invites                  map[string]string // group id -> from
}
type mpGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Max     int      `json:"max"`
	Mode    string   `json:"mode"` // open | invite
	Owner   string   `json:"owner"`
	Members []string `json:"members"`
	Room    string   `json:"-"`
}
type mpRoom struct {
	ID, Region, Name string
	Fixed            bool
	Empty            time.Time
}
type mpServer struct {
	mu      sync.Mutex
	rooms   map[string]*mpRoom
	players map[string]*mpPlayer // by token
	byName  map[string]*mpPlayer // lower name -> online player
	groups  map[string]*mpGroup
	names   map[string]string // lower name -> key hash (reserved forever)
	file    string
	started time.Time
}

var publicURL = func() string { return "" }
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{2,20}$`)

func rid(n int) string      { b := make([]byte, n); rand.Read(b); return hex.EncodeToString(b) }
func khash(k string) string { h := sha256.Sum256([]byte("ct:" + k)); return hex.EncodeToString(h[:12]) }

func newMP(dir string) *mpServer {
	s := &mpServer{rooms: map[string]*mpRoom{}, players: map[string]*mpPlayer{}, byName: map[string]*mpPlayer{}, groups: map[string]*mpGroup{}, names: map[string]string{}, started: time.Now()}
	os.MkdirAll(dir, 0o755)
	s.file = filepath.Join(dir, "mp_names.json")
	if b, err := os.ReadFile(s.file); err == nil {
		json.Unmarshal(b, &s.names)
	}
	// 10 ready-made servers per region, always with the same numbers
	for _, reg := range []string{"EU", "NA", "ASIA", "OCE", "SA"} {
		for i := 0; i < 10; i++ {
			h := sha256.Sum256([]byte(fmt.Sprintf("crosstown-%s-%d", reg, i)))
			id := hex.EncodeToString(h[:2])
			for s.rooms[id] != nil {
				h = sha256.Sum256(h[:])
				id = hex.EncodeToString(h[:2])
			}
			s.rooms[id] = &mpRoom{ID: id, Region: reg, Name: fmt.Sprintf("%s Server #%s", reg, id), Fixed: true, Empty: time.Now()}
		}
	}
	return s
}
func (s *mpServer) addRoom(region string, fixed bool) *mpRoom {
	for {
		id := rid(2)
		if _, ok := s.rooms[id]; ok {
			continue
		}
		r := &mpRoom{ID: id, Region: region, Name: fmt.Sprintf("%s Server #%s", region, id), Fixed: fixed, Empty: time.Now()}
		s.rooms[id] = r
		return r
	}
}
func (s *mpServer) saveNames() {
	b, _ := json.Marshal(s.names)
	os.WriteFile(s.file, b, 0o644)
}
func (s *mpServer) roomCount(id string) int {
	n := 0
	for _, p := range s.players {
		if p.Room == id {
			n++
		}
	}
	return n
}
func (s *mpServer) dropPlayer(p *mpPlayer) {
	s.leaveGroup(p)
	delete(s.players, p.Token)
	if q := s.byName[strings.ToLower(p.Name)]; q == p {
		delete(s.byName, strings.ToLower(p.Name))
	}
	if p.Room != "" {
		s.broadcast(p.Room, mpEvent{K: "left", From: p.Name}, p)
	}
}
func (s *mpServer) leaveGroup(p *mpPlayer) {
	g := s.groups[p.Group]
	p.Group = ""
	if g == nil {
		return
	}
	m := g.Members[:0]
	for _, n := range g.Members {
		if n != p.Name {
			m = append(m, n)
		}
	}
	g.Members = m
	if len(m) == 0 {
		delete(s.groups, g.ID)
		for _, q := range s.players {
			delete(q.Invites, g.ID)
		}
	} else if g.Owner == p.Name {
		g.Owner = m[0]
	}
}
func (s *mpServer) broadcast(room string, ev mpEvent, except *mpPlayer) {
	for _, q := range s.players {
		if q.Room == room && q != except && len(q.Events) < 200 {
			q.Events = append(q.Events, ev)
		}
	}
}
func (s *mpServer) janitor() {
	for {
		time.Sleep(2 * time.Second)
		s.mu.Lock()
		now := time.Now()
		for _, p := range s.players {
			if now.Sub(p.Last) > mpTimeout {
				s.dropPlayer(p)
			}
		}
		for id, r := range s.rooms {
			if s.roomCount(id) > 0 {
				r.Empty = now
			} else if !r.Fixed && now.Sub(r.Empty) > 3*time.Minute {
				delete(s.rooms, id)
			}
		}
		s.mu.Unlock()
	}
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
func cors(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		h(w, r)
	}
}
func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 256*1024)).Decode(v)
}

func regionOrder(r string) int { return strings.Index("EU NA ASIA OCE SA", r) }
func (s *mpServer) roomList(q string) []map[string]any {
	q = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "#")))
	out := []map[string]any{}
	for _, r := range s.rooms {
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) {
			continue
		}
		out = append(out, map[string]any{"id": r.ID, "name": r.Name, "region": r.Region, "players": s.roomCount(r.ID), "max": roomMax})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := regionOrder(out[i]["region"].(string)), regionOrder(out[j]["region"].(string))
		if ri != rj {
			return ri < rj
		}
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

func (s *mpServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("/mp/hello", cors(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"ok": true, "game": "crosstown", "v": 1})
	}))
	mux.HandleFunc("/mp/servers", cors(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		jsonOut(w, map[string]any{"ok": true, "servers": s.roomList(r.URL.Query().Get("q"))})
	}))
	// claim a name (2-20 letters/numbers/_/-). The key proves you own it next time.
	mux.HandleFunc("/mp/login", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Key string }
		if readJSON(r, &in) != nil || len(in.Key) < 8 {
			jsonOut(w, map[string]any{"ok": false, "err": "Bad request"})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if !nameRe.MatchString(in.Name) {
			jsonOut(w, map[string]any{"ok": false, "err": "Names must be 2-20 letters or numbers (_ and - are ok, no spaces)."})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		low := strings.ToLower(in.Name)
		if h, ok := s.names[low]; ok && h != khash(in.Key) {
			jsonOut(w, map[string]any{"ok": false, "err": "That name is already taken. Pick another one."})
			return
		}
		if _, ok := s.names[low]; !ok {
			s.names[low] = khash(in.Key)
			s.saveNames()
		}
		if old := s.byName[low]; old != nil {
			s.dropPlayer(old)
		}
		p := &mpPlayer{Name: in.Name, Token: rid(12), Last: time.Now(), Invites: map[string]string{}}
		s.players[p.Token] = p
		s.byName[low] = p
		jsonOut(w, map[string]any{"ok": true, "token": p.Token, "name": p.Name})
	}))
	mux.HandleFunc("/mp/create", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Token, Region string }
		readJSON(r, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.players[in.Token] == nil {
			jsonOut(w, map[string]any{"ok": false, "err": "Not logged in"})
			return
		}
		reg := strings.ToUpper(in.Region)
		if reg != "EU" && reg != "NA" && reg != "ASIA" && reg != "OCE" && reg != "SA" {
			reg = "EU"
		}
		if len(s.rooms) >= 60 {
			jsonOut(w, map[string]any{"ok": false, "err": "Too many servers right now"})
			return
		}
		rm := s.addRoom(reg, false)
		jsonOut(w, map[string]any{"ok": true, "id": rm.ID, "name": rm.Name})
	}))
	mux.HandleFunc("/mp/join", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Token, Room string }
		readJSON(r, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.players[in.Token]
		rm := s.rooms[strings.ToLower(strings.TrimPrefix(in.Room, "#"))]
		if p == nil || rm == nil {
			jsonOut(w, map[string]any{"ok": false, "err": "That server does not exist anymore"})
			return
		}
		if p.Room != rm.ID && s.roomCount(rm.ID) >= roomMax {
			jsonOut(w, map[string]any{"ok": false, "err": "That server is full"})
			return
		}
		if p.Room != rm.ID {
			s.leaveGroup(p)
			p.Invites = map[string]string{}
			if p.Room != "" {
				s.broadcast(p.Room, mpEvent{K: "left", From: p.Name}, p)
			}
			p.Room = rm.ID
			p.Events = nil
			s.broadcast(rm.ID, mpEvent{K: "joined", From: p.Name}, p)
		}
		p.Last = time.Now()
		jsonOut(w, map[string]any{"ok": true, "id": rm.ID, "name": rm.Name})
	}))
	mux.HandleFunc("/mp/leave", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Token string }
		readJSON(r, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		if p := s.players[in.Token]; p != nil {
			s.leaveGroup(p)
			s.broadcast(p.Room, mpEvent{K: "left", From: p.Name}, p)
			p.Room = ""
		}
		jsonOut(w, map[string]any{"ok": true})
	}))
	// every ~100ms: send my state + events, get everyone else in my server
	mux.HandleFunc("/mp/sync", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string
			S     json.RawMessage
			Ev    []struct {
				To string          `json:"to"`
				K  string          `json:"k"`
				D  json.RawMessage `json:"d"`
			}
		}
		if readJSON(r, &in) != nil {
			jsonOut(w, map[string]any{"ok": false, "err": "Bad request"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.players[in.Token]
		if p == nil || p.Room == "" {
			jsonOut(w, map[string]any{"ok": false, "err": "Lost connection to the server", "relogin": true})
			return
		}
		p.Last = time.Now()
		if len(in.S) > 0 && len(in.S) < 60000 {
			p.State = in.S
		}
		for _, e := range in.Ev {
			if len(e.K) > 16 || len(e.D) > 2048 {
				continue
			}
			ev := mpEvent{K: e.K, From: p.Name, D: e.D}
			if e.To == "*" {
				s.broadcast(p.Room, ev, p)
			} else if e.To == "group" {
				if g := s.groups[p.Group]; g != nil {
					for _, n := range g.Members {
						if q := s.byName[strings.ToLower(n)]; q != nil && q != p && len(q.Events) < 200 {
							q.Events = append(q.Events, ev)
						}
					}
				}
			} else if q := s.byName[strings.ToLower(e.To)]; q != nil && q.Room == p.Room && len(q.Events) < 200 {
				q.Events = append(q.Events, ev)
			}
		}
		others := []map[string]any{}
		for _, q := range s.players {
			if q != p && q.Room == p.Room && q.State != nil {
				others = append(others, map[string]any{"n": q.Name, "g": q.Group, "s": q.State})
			}
		}
		groups := []*mpGroup{}
		for _, g := range s.groups {
			if g.Room == p.Room {
				groups = append(groups, g)
			}
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
		inv := []map[string]string{}
		for gid, from := range p.Invites {
			if g := s.groups[gid]; g != nil {
				inv = append(inv, map[string]string{"g": gid, "name": g.Name, "from": from})
			}
		}
		ev := p.Events
		p.Events = nil
		rm := s.rooms[p.Room]
		rn := ""
		if rm != nil {
			rn = rm.Name
		}
		jsonOut(w, map[string]any{"ok": true, "room": rn, "p": others, "ev": ev, "groups": groups, "group": p.Group, "inv": inv})
	}))
	mux.HandleFunc("/mp/group", cors(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token, Op, Name, Mode, Group, Who string
			Max                               int
		}
		readJSON(r, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.players[in.Token]
		fail := func(m string) { jsonOut(w, map[string]any{"ok": false, "err": m}) }
		if p == nil || p.Room == "" {
			fail("Join a server first")
			return
		}
		switch in.Op {
		case "create":
			n := strings.TrimSpace(in.Name)
			if len([]rune(n)) < 2 || len([]rune(n)) > 24 {
				fail("Group names must be 2-24 characters")
				return
			}
			for _, g := range s.groups {
				if g.Room == p.Room && strings.EqualFold(g.Name, n) {
					fail("A group with that name already exists here")
					return
				}
			}
			if in.Max < 2 {
				in.Max = 2
			}
			if in.Max > 8 {
				in.Max = 8
			}
			if in.Mode != "invite" {
				in.Mode = "open"
			}
			s.leaveGroup(p)
			g := &mpGroup{ID: rid(4), Name: n, Max: in.Max, Mode: in.Mode, Owner: p.Name, Members: []string{p.Name}, Room: p.Room}
			s.groups[g.ID] = g
			p.Group = g.ID
		case "join", "accept":
			g := s.groups[in.Group]
			if g == nil || g.Room != p.Room {
				fail("That group is gone")
				return
			}
			if g.Mode == "invite" && p.Invites[g.ID] == "" {
				fail("This group is invite only")
				return
			}
			if len(g.Members) >= g.Max {
				fail("That group is full")
				return
			}
			if p.Group != g.ID {
				s.leaveGroup(p)
				g.Members = append(g.Members, p.Name)
				p.Group = g.ID
			}
			delete(p.Invites, g.ID)
		case "decline":
			delete(p.Invites, in.Group)
		case "leave":
			s.leaveGroup(p)
		case "invite":
			g := s.groups[p.Group]
			q := s.byName[strings.ToLower(in.Who)]
			if g == nil {
				fail("You are not in a group")
				return
			}
			if q == nil || q.Room != p.Room {
				fail("That player is not on this server")
				return
			}
			if q.Group == g.ID {
				fail("They are already in your group")
				return
			}
			q.Invites[g.ID] = p.Name
			q.Events = append(q.Events, mpEvent{K: "invite", From: p.Name, D: json.RawMessage(fmt.Sprintf("%q", g.Name))})
		case "kick":
			g := s.groups[p.Group]
			q := s.byName[strings.ToLower(in.Who)]
			if g == nil || g.Owner != p.Name || q == nil || q.Group != g.ID || q == p {
				fail("You can't kick them")
				return
			}
			s.leaveGroup(q)
			q.Events = append(q.Events, mpEvent{K: "kicked", From: p.Name})
		default:
			fail("Unknown action")
			return
		}
		jsonOut(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/mp/status", cors(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		jsonOut(w, map[string]any{"ok": true, "servers": s.roomList(""), "online": len(s.players), "addr": lanIPs(), "port": mpPort, "url": publicURL()})
	}))
}

func lanIPs() []string {
	out := []string{}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil {
				out = append(out, ip.IP.String())
			}
		}
	}
	return out
}
