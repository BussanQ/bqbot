#!/bin/sh
# Install the same desktop in fresh images and existing managed containers.
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get -o Acquire::Retries=2 update
apt-get -o Acquire::Retries=2 install -y --no-install-recommends xfce4-panel xfdesktop4 thunar adwaita-icon-theme
rm -rf /var/lib/apt/lists/*
install -D -m 0644 /opt/bqbot/workspace-browser.desktop /usr/local/share/applications/workspace-browser.desktop
