package main

// Crosstown online server (runs on Render.com or any computer):
// ready-made servers (10 EU, 10 NA, 10 ASIA, 10 OCE, 10 SA), unique names, groups.

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "7777"
	}
	dl := strings.TrimSpace(os.Getenv("DOWNLOAD_URL"))
	s := newMP(os.TempDir())
	go s.janitor()
	mux := http.NewServeMux()
	s.routes(mux)
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if dl == "" {
			http.Error(w, "The download is not set up yet.", 404)
			return
		}
		http.Redirect(w, r, dl, http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.ReplaceAll(page, "{{DL}}", dl))
	})
	log.Printf("Crosstown server on port %s", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), mux))
}

const page = `<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Crosstown</title>
<style>*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(ellipse at top,#3b1d5e,#0b0d14 60%);color:#eee;font:16px Segoe UI,Arial;display:flex;align-items:center;justify-content:center;padding:24px}
.c{max-width:680px;width:100%;text-align:center}h1{font-size:64px;margin:0;letter-spacing:6px;background:linear-gradient(90deg,#ff5fa2,#ffd23f);-webkit-background-clip:text;color:transparent}
.sub{opacity:.7;letter-spacing:4px;margin-bottom:28px}a.b{display:inline-block;background:#ffd23f;color:#111;font-weight:800;letter-spacing:2px;padding:18px 38px;border-radius:12px;text-decoration:none;font-size:20px}
.st{text-align:left;background:#ffffff0d;border:1px solid #ffffff1a;border-radius:12px;padding:18px 22px;margin-top:26px;line-height:1.8}.st b{color:#ffd23f}
textarea{width:100%;height:90px;margin-top:8px;background:#000a;color:#7ee787;border:1px solid #ffffff22;border-radius:8px;padding:8px;font:13px Consolas,monospace;resize:none}
button{margin-top:8px;background:#ffd23f;border:0;border-radius:8px;padding:8px 16px;font-weight:800;cursor:pointer}.on{margin-top:16px;opacity:.75;font-size:14px}</style></head>
<body><div class="c"><h1>CROSSTOWN</h1><div class="sub">PALM COAST • ONLINE</div><a class="b" href="/download">DOWNLOAD THE GAME</a>
<div class="st"><b>Install with one command</b> - press the Windows key, type <b>cmd</b>, open it, paste this (right click) and press Enter. The game goes into your <b>Downloads</b> folder and starts:<textarea id="cmd" readonly onclick="this.select()"></textarea><button onclick="navigator.clipboard.writeText(document.getElementById('cmd').value);this.textContent='Copied!'">Copy</button></div>
<div class="st"><b>Play online</b><br>Open <b>Crosstown.exe</b> → <b>Multiplayer</b> → pick a server (EU, NA, ASIA, OCE, SA) → <b>Join</b>.</div><div class="on" id="on"></div></div>
<script>const D='{{DL}}';document.getElementById('cmd').value='cd /d "%USERPROFILE%\\Downloads" && curl -L -o Crosstown.zip '+(D||location.origin+'/download')+' && tar -xf Crosstown.zip && del Crosstown.zip && start "" "Crosstown\\Crosstown.exe" && explorer Crosstown';
fetch('/mp/status').then(r=>r.json()).then(r=>{document.getElementById('on').textContent=r.online+' player'+(r.online==1?'':'s')+' online right now'}).catch(()=>{});</script></body></html>`
