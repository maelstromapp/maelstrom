#!/bin/bash -xe

role="maelnode"

# create systemd unit
cat <<EOF > /etc/systemd/system/maelstromd.service
[Unit]
Description=maelstromd
After=docker.service
[Service]
TimeoutStartSec=0
Restart=always
RestartSec=5
ExecStartPre=/bin/mkdir -p /var/maelstrom
ExecStartPre=/bin/chmod 700 /var/maelstrom
ExecStart=/usr/bin/maelstromd -sqlDriver ${dbDriver} -sqlDSN ${dbDSN}
[Install]
WantedBy=multi-user.target
EOF

# set hostname
instanceId=`curl -s http://169.254.169.254/latest/meta-data/instance-id`
hostname="${role}-${instanceId}"
sudo hostname ${hostname}
sudo bash -c "echo ${hostname} > /etc/hostname"

# start docker
systemctl restart docker

# start maelstromd
systemctl daemon-reload
systemctl enable maelstromd
systemctl start maelstromd
