#!/bin/sh
set -eu
if [ "$#" = 0 ]; then
  : "${BQBOT_WORKSPACE_TOKEN:?Set a private Workspace Bridge token}"
  [ "${#BQBOT_WORKSPACE_TOKEN}" -ge 32 ] || { echo "Workspace token needs at least 32 characters" >&2; exit 1; }
  digest=$(printf %s "Bearer $BQBOT_WORKSPACE_TOKEN" | sha256sum)
  export BQBOT_WORKSPACE_HASH=${digest%% *}
  unset BQBOT_WORKSPACE_TOKEN
  exec /usr/bin/tini -- /bin/sh "$0" supervise
fi
if [ "$1" = supervise ]; then
  # Finish Chromium's profile writes before tearing down D-Bus and the X server.
  setsid dbus-run-session -- /bin/sh "$0" desktop &
  session=$!
  stop_session() {
    trap - TERM INT
    python3 /opt/bqbot/desktop.py --close-browser || true
    kill -TERM "-$session" 2>/dev/null || true
    wait "$session" || true
    exit 0
  }
  trap stop_session TERM INT
  wait "$session"
  exit $?
fi
if [ "$1" = desktop ]; then
  # One desktop owns a persistent profile at a time, including across containers.
  exec flock -n -F /home/agent/profile/.bqbot-owner /bin/sh "$0" session
fi
# Chromium's lock names refer to the previous container's hostname and /tmp.
# With the volume locked by this process they can safely be recreated.
rm -f /tmp/.X1-lock /tmp/.X11-unix/X1
rm -f /home/agent/profile/SingletonLock /home/agent/profile/SingletonSocket /home/agent/profile/SingletonCookie
export XDG_RUNTIME_DIR=/tmp/bqbot-runtime GTK_MODULES=gail:atk-bridge NO_AT_BRIDGE=0
mkdir -pm 700 "$XDG_RUNTIME_DIR"
# Keep the management token out of the browser, window manager and their children.
desktop() { env -u BQBOT_WORKSPACE_HASH "$@"; }
desktop Xtigervnc :1 -geometry 1280x800 -depth 24 -SecurityTypes None -localhost yes -rfbport 5901 -ac -nolisten tcp &
i=0
until xdpyinfo >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 100 ] || exit 1; sleep 0.1; done
for property in IsEnabled ScreenReaderEnabled; do
  dbus-send --session --print-reply --dest=org.a11y.Bus /org/a11y/bus \
    org.freedesktop.DBus.Properties.Set string:org.a11y.Status string:"$property" variant:boolean:true >/dev/null
done
export XDG_CURRENT_DESKTOP=XFCE XDG_MENU_PREFIX=xfce-
panel_dir="${XDG_CONFIG_HOME:-$HOME/.config}/xfce4/xfconf/xfce-perchannel-xml"
mkdir -p "$panel_dir"
[ -f "$panel_dir/xfce4-panel.xml" ] || cp /opt/bqbot/panel.xml "$panel_dir/xfce4-panel.xml"
desktop openbox > /tmp/window-manager.log 2>&1 &
# Xvnc exposes one monitor. Use the shipped JPEG as the initial wallpaper.
background=/backdrop/screen0/monitorVNC-0/workspace0/last-image
if ! desktop xfconf-query -c xfce4-desktop -p "$background" >/dev/null 2>&1; then
  desktop xfconf-query -c xfce4-desktop -p "$background" -n -t string -s /usr/share/backgrounds/xfce/xfce-blue.jpg
fi
desktop xfdesktop >/tmp/desktop.log 2>&1 &
desktop xfce4-panel --disable-wm-check >/tmp/panel.log 2>&1 &
# Openbox publishes the remaining desktop area after both panels reserve space.
i=0
until xprop -root _NET_WORKAREA | awk -F '[=,]' 'NF >= 5 && $3 > 0 && $3 + $5 < 800 {ready=1} END {exit !ready}'; do
  i=$((i+1)); [ "$i" -lt 100 ] || { echo "Desktop panels did not start; see /tmp/panel.log" >&2; exit 1; }
  sleep 0.1
done
desktop chromium --no-sandbox --disable-dev-shm-usage --no-first-run --disable-sync \
  --disable-background-networking --force-renderer-accessibility --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 --user-data-dir=/home/agent/profile --window-position=112,70 \
  --window-size=1056,628 about:blank >/tmp/browser.log 2>&1 &
python3 - <<'PY'
import time
from urllib.request import urlopen
for attempt in range(100):
    try:
        with urlopen("http://127.0.0.1:9222/json/version", timeout=1):
            break
    except OSError:
        time.sleep(.15)
else:
    raise SystemExit("Chromium failed to start. See /tmp/browser.log in the workspace.")
PY
exec bqbot-workspace
