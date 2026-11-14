ARG BASE_IMAGE=debian:trixie-slim
FROM ${BASE_IMAGE} AS desktop
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates chromium tigervnc-standalone-server openbox x11-utils xdotool xclip xterm \
    dbus-x11 at-spi2-core python3-pyatspi python3-websocket python3-pil python3-jsonschema \
    ffmpeg fonts-liberation fonts-noto-cjk tini && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 agent && mkdir -p /home/agent/workspace /home/agent/profile && chown -R agent:agent /home/agent && install -d -m 1777 /tmp/.X11-unix
FROM desktop
COPY workspace/install-desktop.sh workspace/workspace-browser.desktop /opt/bqbot/
RUN sh /opt/bqbot/install-desktop.sh
LABEL dev.bqbot.desktop="xfce-files-v1"
ARG TARGETARCH
ARG BRIDGE_DIR=.build
COPY workspace/${BRIDGE_DIR}/bridge-${TARGETARCH} /usr/local/bin/bqbot-workspace
COPY workspace/desktop.py workspace/start.sh workspace/panel.xml internal/computer/workspace-action.json /opt/bqbot/
USER agent
WORKDIR /home/agent/workspace
ENV DISPLAY=:1 HOME=/home/agent
ENTRYPOINT ["/bin/sh","/opt/bqbot/start.sh"]
CMD []
