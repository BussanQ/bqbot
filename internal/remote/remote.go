// Package remote provides a deliberately limited, paired browser chat gateway.
package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type Backend interface {
	Bots() ([]store.Bot, error)
	Transcript(string) ([]store.Message, error)
	Send(string, string, []driver.Attachment, string) (store.Message, error)
}
type session struct {
	expires time.Time
	bots    map[string]bool
}

// Gateway never exposes the local service token, settings, or approval endpoints.
type Gateway struct {
	mu       sync.Mutex
	backend  Backend
	origin   string
	server   *http.Server
	address  string
	code     [32]byte
	expires  time.Time
	attempts int
	bots     map[string]bool
	sessions map[[32]byte]session
}
type State struct {
	Enabled bool   `json:"enabled"`
	Origin  string `json:"origin,omitempty"`
	Address string `json:"address,omitempty"`
	Devices int    `json:"devices"`
}

func Listen(backend Backend, address, origin string) (*Gateway, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("remote origin must be an HTTPS origin without a path")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("remote gateway must listen on a literal loopback address")
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	g := &Gateway{backend: backend, origin: origin, address: ln.Addr().String(), sessions: map[[32]byte]session{}}
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go g.server.Serve(ln)
	return g, nil
}
func (g *Gateway) Close() error { g.Revoke(); return g.server.Close() }
func (g *Gateway) Revoke() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions = map[[32]byte]session{}
	g.expires = time.Time{}
}
func (g *Gateway) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, s := range g.sessions {
		if time.Now().After(s.expires) {
			delete(g.sessions, key)
		}
	}
	return State{true, g.origin, g.address, len(g.sessions)}
}

// PairCode creates a five-minute, one-use code scoped to existing bots.
func (g *Gateway) PairCode(ids []string) (string, error) {
	bots, err := g.backend.Bots()
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		for _, b := range bots {
			if b.ID == id {
				allowed[id] = true
			}
		}
	}
	if len(ids) == 0 || len(allowed) != len(ids) {
		return "", errors.New("select distinct existing bots for this device")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(9000000000))
	if err != nil {
		return "", err
	}
	code := new(big.Int).Add(n, big.NewInt(1000000000)).String()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.code = sha256.Sum256([]byte(code))
	g.expires = time.Now().Add(5 * time.Minute)
	g.attempts = 0
	g.bots = allowed
	return code, nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func reject(w http.ResponseWriter, status int) {
	respond(w, status, map[string]string{"error": http.StatusText(status)})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		reject(w, 415)
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		reject(w, 400)
		return false
	}
	return true
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if "https://"+r.Host != g.origin {
		reject(w, 403)
		return
	}
	if origin := r.Header.Get("Origin"); (origin != "" && origin != g.origin) || (r.Method != "GET" && origin != g.origin) {
		reject(w, 403)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/app.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		io.WriteString(w, script)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/pair" {
		g.pair(w, r)
		return
	}
	cookie, err := r.Cookie("bqbot_device")
	if err != nil {
		reject(w, 401)
		return
	}
	key := sha256.Sum256([]byte(cookie.Value))
	g.mu.Lock()
	s, ok := g.sessions[key]
	if time.Now().After(s.expires) {
		delete(g.sessions, key)
		ok = false
	}
	g.mu.Unlock()
	if !ok {
		reject(w, 401)
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/logout":
		g.mu.Lock()
		delete(g.sessions, key)
		g.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "bqbot_device", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		respond(w, 200, struct{}{})
	case r.Method == "GET" && r.URL.Path == "/bots":
		bots, err := g.backend.Bots()
		if err != nil {
			reject(w, 502)
			return
		}
		out := []map[string]string{}
		for _, b := range bots {
			if s.bots[b.ID] {
				out = append(out, map[string]string{"id": b.ID, "name": b.Name})
			}
		}
		respond(w, 200, out)
	case r.Method == "GET" && r.URL.Path == "/transcript":
		id := r.URL.Query().Get("bot")
		if !s.bots[id] {
			reject(w, 403)
			return
		}
		msgs, err := g.backend.Transcript(id)
		if err != nil {
			reject(w, 502)
			return
		}
		if len(msgs) > 200 {
			msgs = msgs[len(msgs)-200:]
		}
		out := []map[string]string{}
		for _, m := range msgs {
			out = append(out, map[string]string{"role": m.Role, "text": m.Text})
		}
		respond(w, 200, out)
	case r.Method == "POST" && r.URL.Path == "/send":
		var req struct {
			Bot  string `json:"bot"`
			Text string `json:"text"`
		}
		if !decode(w, r, &req) {
			return
		}
		if !s.bots[req.Bot] {
			reject(w, 403)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			reject(w, 400)
			return
		}
		msg, err := g.backend.Send(req.Bot, req.Text, nil, "")
		if err != nil {
			reject(w, 502)
			return
		}
		respond(w, 202, map[string]string{"id": msg.ID})
	default:
		reject(w, 404)
	}
}
func (g *Gateway) pair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	hash := sha256.Sum256([]byte(req.Code))
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Now().After(g.expires) || g.attempts >= 5 {
		reject(w, 401)
		return
	}
	g.attempts++
	if subtle.ConstantTimeCompare(hash[:], g.code[:]) != 1 {
		reject(w, 401)
		return
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		reject(w, 500)
		return
	}
	token := hex.EncodeToString(secret[:])
	g.sessions[sha256.Sum256([]byte(token))] = session{time.Now().Add(24 * time.Hour), g.bots}
	g.expires = time.Time{}
	http.SetCookie(w, &http.Cookie{Name: "bqbot_device", Value: token, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	respond(w, 200, struct{}{})
}

const page = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>bqbot 远程聊天</title><body><h1>bqbot</h1><p>仅访问桌面授权的伙伴。工具审批请在桌面处理。</p><form id="pair"><label>配对码 <input name="code" inputmode="numeric" autocomplete="one-time-code" required></label><button>配对</button></form><section id="chat" hidden><label>伙伴 <select id="bots"></select></label><div id="messages" aria-live="polite"></div><form id="send"><label>消息 <textarea name="text" required maxlength="16000"></textarea></label><button>发送</button></form><button id="logout">断开设备</button></section><p id="status" role="status"></p><script src="/app.js"></script></body></html>`
const script = `const $=id=>document.getElementById(id);
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(r.status===401?'请重新配对':r.status===403?'此伙伴未授权':'请求失败');return r.json();}
function fail(e){$('status').textContent=e.message;}
async function refresh(){const id=$('bots').value;if(!id)return;const messages=await call('/transcript?bot='+encodeURIComponent(id));if($('bots').value!==id)return;$('messages').replaceChildren(...messages.map(m=>{const p=document.createElement('p');p.textContent=(m.role==='user'?'你：':'伙伴：')+m.text;return p;}));}
async function connect(){const bots=await call('/bots');$('bots').replaceChildren(...bots.map(b=>{const o=document.createElement('option');o.value=b.id;o.textContent=b.name;return o;}));$('pair').hidden=true;$('chat').hidden=false;await refresh();}
$('pair').onsubmit=async e=>{e.preventDefault();try{await call('/pair',{code:e.target.code.value});e.target.reset();await connect();$('status').textContent='已连接';}catch(e){fail(e);}};
$('send').onsubmit=async e=>{e.preventDefault();const button=e.target.querySelector('button');button.disabled=true;try{await call('/send',{bot:$('bots').value,text:e.target.text.value});e.target.reset();await refresh();}catch(e){fail(e);}finally{button.disabled=false;}};
$('bots').onchange=()=>refresh().catch(fail);$('logout').onclick=async()=>{try{await call('/logout',{});location.reload();}catch(e){fail(e);}};
setInterval(()=>{if(!$('chat').hidden&&!document.hidden)refresh().catch(fail);},2500);connect().catch(()=>{});`
