#!/usr/bin/env bash
set -exo pipefail
if [ "$EUID" -ne 0 ]; then
  echo "Please run as root using sudo create-plist.bash"
  exit 1
fi
launchctl unload -w /Library/LaunchDaemons/com.veertu.anklet.plist || true
# Create the plist file
cat <<EOF > /Library/LaunchDaemons/com.veertu.anklet.plist
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.veertu.anklet</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/anklet</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/tmp/</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>AssociatedBundleIdentifiers</key>
    <string>com.veertu.anklet</string>
</dict>
</plist>
EOF
# install anklet
curl -O -L https://github.com/veertuinc/anklet/releases/download/v0.2.0/anklet-v0.2.0-darwin-arm64.zip
unzip anklet-v0.2.0-darwin-arm64.zip
chmod +x anklet-v0.2.0-darwin-arm64
sudo mv anklet-v0.2.0-darwin-arm64 /usr/local/bin/anklet
# create config
mkdir -p ~/.config/anklet
touch ~/.config/anklet/config.yml
if [ ! -f ~/.config/anklet/config.yml ]; then
  cat <<EOF > ~/.config/anklet/config.yml
---
work_dir: /tmp/
pid_file_dir: /tmp/
#log:
  # if file_dir is not set, it will be set to current directory you execute anklet in
  #file_dir: /Users/nathanpierce/Library/Logs/
#metrics:
  #port: 8081
services:
  # . . . populate this with your services
EOF
fi
# load the plist
launchctl load -w /Library/LaunchDaemons/com.veertu.anklet.plist
echo "Anklet has been installed and loaded."
echo "You can control it with the following commands:"
echo "  sudo launchctl start com.veertu.anklet"
echo "  sudo launchctl stop com.veertu.anklet"
echo "  sudo launchctl unload -w /Library/LaunchDaemons/com.veertu.anklet.plist"
echo "  sudo launchctl load -w /Library/LaunchDaemons/com.veertu.anklet.plist"