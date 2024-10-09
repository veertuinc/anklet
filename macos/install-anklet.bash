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
ARCH=$([[ $(arch) == "arm64" ]] && echo "arm64" || echo "amd64")
LATEST_VERSION=$(curl -sL https://api.github.com/repos/veertuinc/anklet/releases | jq -r '.[0].tag_name')
curl -L -O https://github.com/veertuinc/anklet/releases/download/${LATEST_VERSION}/anklet_${LATEST_VERSION}_darwin_${ARCH}.zip
unzip anklet_${LATEST_VERSION}_darwin_${ARCH}.zip
chmod +x anklet_${LATEST_VERSION}_darwin_${ARCH}
cp anklet_${LATEST_VERSION}_darwin_${ARCH} /usr/local/bin/anklet
mkdir -p ~/.config/
sudo chown -R $AWS_INSTANCE_USER:staff ~/.config
cd ~/.config/
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
plugins:
  # . . . populate this with your plugins
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
