"""A bounded set of operations inside one Linux desktop. JSON in, MCP content out."""
import base64
import hashlib
import io
import json
import os
import socket
import struct
import subprocess
import sys
import time
from contextlib import contextmanager
from urllib.parse import urlsplit
from urllib.request import ProxyHandler, build_opener

SCHEMA = "/opt/bqbot/workspace-action.json"
CDP = "http://127.0.0.1:9222"


def text(value):
    if not isinstance(value, str):
        value = json.dumps(value, ensure_ascii=False)
    return [{"type": "text", "text": value}]


class RFB:
    """Shared raw framebuffer and pointer events on our loopback Xvnc server."""
    def __init__(self, sock):
        self.sock = sock
        if self.read(12) != b"RFB 003.008\n":
            raise ValueError("Xvnc does not offer RFB 3.8")
        sock.sendall(b"RFB 003.008\n")
        methods = self.read(self.read(1)[0])
        if 1 not in methods:
            raise ValueError("Unexpected Xvnc security type")
        sock.sendall(b"\x01")
        if self.read(4) != b"\0" * 4:
            raise ValueError("Xvnc rejected the connection")
        sock.sendall(b"\x01")  # share the screen with other viewers
        self.width, self.height = struct.unpack(">HH", self.read(4))
        if not 0 < self.width * self.height <= 4096 * 4096:
            raise ValueError("Unsupported framebuffer dimensions")
        self.read(16)
        self.read(struct.unpack(">I", self.read(4))[0])

    def read(self, size):
        if size > 64 << 20:
            raise ValueError("Invalid RFB packet length")
        data = bytearray()
        while len(data) < size:
            chunk = self.sock.recv(min(65536, size-len(data)))
            if not chunk:
                raise ValueError("Xvnc disconnected")
            data.extend(chunk)
        return bytes(data)

    def pointer(self, action):
        x, y = action.get("x", 0), action.get("y", 0)
        if not (0 <= x < self.width and 0 <= y < self.height):
            raise ValueError("Coordinate outside the framebuffer")
        mask = 1 if action["kind"] == "click" else {"up":8, "down":16}[action["direction"]]
        for buttons in (mask, 0):
            self.sock.sendall(struct.pack(">BBHH", 5, buttons, x, y))
        return text("OK")

    def capture(self):
        from PIL import Image
        self.sock.sendall(struct.pack(">B3x4B3H3B3x", 0, 32, 24, 0, 1, 255, 255, 255, 16, 8, 0))
        self.sock.sendall(struct.pack(">BxHi", 2, 1, 0))  # raw encoding
        self.sock.sendall(struct.pack(">BBHHHH", 3, 0, 0, 0, self.width, self.height))
        image = Image.new("RGB", (self.width, self.height))
        while True:
            msg = self.read(1)[0]
            if msg == 2:  # bell
                continue
            if msg == 3:  # server clipboard
                self.read(3)
                self.read(struct.unpack(">I", self.read(4))[0])
                continue
            if msg != 0:
                raise ValueError("Unsupported RFB packet")
            count = struct.unpack(">xH", self.read(3))[0]
            if not count:
                continue
            for _ in range(count):
                x, y, w, h, encoding = struct.unpack(">HHHHi", self.read(12))
                if encoding != 0 or not w or not h or x+w > self.width or y+h > self.height:
                    raise ValueError("Invalid framebuffer rectangle")
                tile = Image.frombytes("RGB", (w, h), self.read(w*h*4), "raw", "BGRX")
                image.paste(tile, (x, y))
            break
        data = io.BytesIO()
        image.save(data, format="JPEG", quality=82)
        return [{"type":"image", "mimeType":"image/jpeg", "data":base64.b64encode(data.getvalue()).decode()}]


def tabs():
    http = build_opener(ProxyHandler({}))
    with http.open(CDP+"/json/list", timeout=3) as response:
        return [t for t in json.load(response) if t.get("type") == "page"]


class Page:
    def __init__(self, sock):
        self.sock = sock
        self.seq = 0

    def call(self, method, params=None):
        self.seq += 1
        self.sock.send(json.dumps({"id":self.seq, "method":method, "params":params or {}}))
        while True:
            reply = json.loads(self.sock.recv())
            if reply.get("id") == self.seq:
                if "error" in reply:
                    raise ValueError(reply["error"].get("message", "CDP command failed"))
                return reply.get("result", {})

    def evaluate(self, source, argument=None):
        expression = "(" + source + ")(" + json.dumps(argument) + ")"
        result = self.call("Runtime.evaluate", {"expression":expression,"returnByValue":True})
        if result.get("exceptionDetails"):
            raise ValueError("Element is missing or the selector is invalid")
        return result["result"].get("value")


@contextmanager
def connect_tab(target):
    import websocket
    pages = tabs()
    page = next((t for t in pages if not target or t["id"] == target), None)
    if not page:
        raise ValueError("Unknown browser tab")
    url = page["webSocketDebuggerUrl"]
    if urlsplit(url).hostname not in ("localhost","127.0.0.1") or urlsplit(url).port != 9222:
        raise ValueError("Invalid CDP endpoint")
    sock = websocket.create_connection(url, timeout=5, suppress_origin=True)
    try:
        yield Page(sock)
    finally:
        sock.close()


READ_PAGE = """() => {
  function selector(el) {
    if (el.id && document.querySelectorAll('#'+CSS.escape(el.id)).length === 1) return '#'+CSS.escape(el.id);
    const path = [];
    while (el && el.parentElement) {
      const peers = [...el.parentElement.children].filter(n => n.localName === el.localName);
      path.unshift(el.localName+':nth-of-type('+(peers.indexOf(el)+1)+')');
      el = el.parentElement;
    }
    return path.join('>');
  }
  const items = [...document.querySelectorAll('button,a[href],input,textarea,select,[role=button]')]
    .filter(e => e.getClientRects().length).slice(0,60).map(e => ({
      label: (e.getAttribute('aria-label') || e.innerText || e.placeholder || e.type || '').trim().slice(0,100),
      selector: selector(e).slice(0,1024)
    }));
  return {url:location.href,title:document.title,items,text:(document.body?.innerText || '').slice(0,6000)};
}"""


def browser(action):
    kind = action["kind"]
    if kind == "browser_tabs":
        return text([{k:t[k] for k in ("id","title","url")} for t in tabs()][:64])
    with connect_tab(action.get("target")) as page:
        if kind == "browser_open":
            url = action.get("url", "")
            parsed = urlsplit(url)
            if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password:
                raise ValueError("Only HTTP(S) pages without embedded credentials can be opened")
            result = page.call("Page.navigate", {"url":url})
            if result.get("errorText"):
                raise ValueError(result["errorText"])
            return text({"url":url, "frame":result.get("frameId")})
        if kind == "browser_read":
            return text(page.evaluate(READ_PAGE))
        selector = action["selector"]
        if kind == "browser_fill":
            if not page.evaluate("""s => {
                const el = document.querySelector(s);
                if (!el || el.disabled || el.readOnly || !(el.isContentEditable || el.matches('input,textarea'))) return false;
                el.focus(); if(el.select) el.select();
                else {const r=document.createRange();r.selectNodeContents(el);const sel=getSelection();sel.removeAllRanges();sel.addRange(r);}
                return true;
            }""", selector):
                raise ValueError("Element is not editable")
            page.call("Input.insertText", {"text":action.get("text","")})
        else:
            pos = page.evaluate("""s => {
                const el=document.querySelector(s); if(!el || el.disabled)return null; el.scrollIntoView({block:'center'});
                const r=el.getBoundingClientRect(), x=r.left+r.width/2, y=r.top+r.height/2;
                return el.contains(document.elementFromPoint(x,y)) ? {x,y} : null;
            }""", selector)
            if not pos:
                raise ValueError("Element is not visible or is covered")
            for event in ("mousePressed","mouseReleased"):
                page.call("Input.dispatchMouseEvent", {"type":event,"button":"left","clickCount":1,**pos})
        return text("OK")


def reference(path, node):
    name = node.getRoleName()+"\0"+(node.name or "")
    return ".".join(map(str,path))+"~"+hashlib.sha256(name.encode()).hexdigest()[:12]


def accessibility(action):
    import pyatspi
    desktop = pyatspi.Registry.getDesktop(0)
    if action["kind"] == "gui_tree":
        rows = []
        def visit(node, path):
            if node is None or len(rows) >= 160 or len(path)>16:
                return
            try:
                try:
                    invokable = node.queryAction().nActions > 0
                except (NotImplementedError, RuntimeError):
                    invokable = False
                rows.append({"ref":reference(path,node),"role":node.getRoleName(),"name":(node.name or "")[:120],"invokable":invokable})
                for i in range(min(node.childCount, 80)):
                    if len(rows) >= 160:
                        break
                    visit(node.getChildAtIndex(i), path+[i])
            except (RuntimeError, LookupError):
                pass  # the accessible may have vanished during traversal
        visit(desktop, [])
        return text(rows)
    ref = action["ref"]
    if "~" not in ref:
        raise ValueError("Use a current reference from gui_tree")
    raw_path = ref.split("~",1)[0]
    path = [int(n) for n in raw_path.split(".") if n]
    node = desktop
    for i in path:
        if i < 0 or i >= node.childCount:
            raise ValueError("Element is stale; refresh gui_tree")
        node = node.getChildAtIndex(i)
    if node is None or reference(path,node) != ref:
        raise ValueError("Element is stale; refresh gui_tree")
    operations = node.queryAction()
    if not operations.nActions or not operations.doAction(0):
        raise ValueError("Element has no invokable action")
    return text("OK")


def execute(action):
    from jsonschema import validate
    with open(SCHEMA, encoding="utf-8") as f:
        validate(action, json.load(f))
    kind = action["kind"]
    if kind.startswith("browser_"):
        return browser(action)
    if kind.startswith("gui_"):
        return accessibility(action)
    if kind in ("click","scroll","screenshot"):
        with socket.create_connection(("127.0.0.1",5901), timeout=5) as sock:
            desktop = RFB(sock)
            return desktop.capture() if kind == "screenshot" else desktop.pointer(action)
    if kind == "key":
        cmd = ["xdotool","key","--clearmodifiers",action["key"]]
    else:
        if not action.get("text") or "\0" in action["text"]:
            raise ValueError("Missing input text")
        cmd = ["xdotool","type","--clearmodifiers","--delay","1","--",action["text"]]
    subprocess.run(cmd, check=True, timeout=12, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return text("OK")


if __name__ == "__main__":
    if sys.argv[1:] == ["--close-browser"]:
        # This lifecycle operation is not part of the MCP tool vocabulary.
        import websocket
        lock = "/home/agent/profile/SingletonLock"
        try:
            pid = int(os.readlink(lock).rsplit("-", 1)[1])
        except (OSError, IndexError, ValueError):
            pid = None
        try:
            with build_opener(ProxyHandler({})).open(CDP+"/json/version", timeout=2) as response:
                url = json.load(response)["webSocketDebuggerUrl"]
            sock = websocket.create_connection(url, timeout=2, suppress_origin=True)
            try:
                sock.send(json.dumps({"id":1, "method":"Browser.close"}))
                sock.recv()
            finally:
                sock.close()
        except (OSError, websocket.WebSocketException):
            pass
        # The lock can disappear before Chromium finishes flushing preferences.
        for _ in range(100):
            if pid is not None:
                try:
                    with open(f"/proc/{pid}/stat", encoding="utf-8") as f:
                        if f.read().rsplit(")", 1)[1].split()[0] == "Z":
                            break
                except FileNotFoundError:
                    break
            elif not os.path.lexists(lock):
                break
            time.sleep(.1)
        sys.exit(0)
    try:
        result = {"content":execute(json.load(sys.stdin))}
    except Exception as error:
        result = {"isError":True, "content":text(str(error)[:400])}
    print(json.dumps(result))
