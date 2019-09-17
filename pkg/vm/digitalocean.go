package vm

import (
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/config"
	"github.com/digitalocean/godo"
	"github.com/mgutz/logxi/v1"
	"golang.org/x/oauth2"
	"io/ioutil"
	"strconv"
	"time"
)

func NewDOAdapter(accessToken string, ctx *context.Context) *DOAdapter {
	tokenSource := &TokenSource{
		AccessToken: accessToken,
	}
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)
	client := godo.NewClient(oauthClient)
	return &DOAdapter{
		client: client,
		ctx:    ctx,
	}
}

type DOAdapter struct {
	client *godo.Client
	ctx    *context.Context
}

func (d *DOAdapter) Name() string {
	return "digitalocean"
}

func (d *DOAdapter) CreateCluster(cfg config.Config, opts CreateClusterOptions) (CreateClusterOut, error) {
	if cfg.DigitalOcean == nil {
		return CreateClusterOut{}, fmt.Errorf("createCluster: opts.DigitalOcean cannot be nil")
	}

	if cfg.DigitalOcean.Region == "" {
		return CreateClusterOut{}, fmt.Errorf("createCluster: opts.DigitalOcean.Region cannot be empty")
	}

	// register ssh key
	keyName := fmt.Sprintf("%s_sshkey", cfg.Cluster.Name)
	clusterTag := fmt.Sprintf("cluster:%s", cfg.Cluster.Name)
	dropletTag := fmt.Sprintf("cluster:vm:%s", cfg.Cluster.Name)

	// create tags
	log.Info("digitalocean: creating tags")
	tags := []string{clusterTag, dropletTag}
	for _, tag := range tags {
		_, _, err := d.client.Tags.Create(*d.ctx, &godo.TagCreateRequest{Name: tag})
		if err != nil {
			return CreateClusterOut{}, fmt.Errorf("createCluster: failed to create tag: %s - %v", tag, err)
		}
	}

	// register ssh key
	publicKey, err := ioutil.ReadFile(opts.SSHPublicKeyFile)
	if err != nil {
		return CreateClusterOut{}, fmt.Errorf("createCluster: unable to read file: %s - %v",
			opts.SSHPublicKeyFile, err)
	}

	log.Info("digitalocean: registering ssh key")
	sshKey, _, err := d.client.Keys.Create(*d.ctx, &godo.KeyCreateRequest{
		Name:      keyName,
		PublicKey: string(publicKey),
	})
	if err != nil {
		return CreateClusterOut{}, fmt.Errorf("createCluster: unable to create ssh key: %s - %v", keyName, err)
	}
	cfg.DigitalOcean.SSHFingerprint = sshKey.Fingerprint

	// create load balancer
	log.Info("digitalocean: creating load balancer")
	_, _, err = d.client.LoadBalancers.Create(*d.ctx, &godo.LoadBalancerRequest{
		Name:      fmt.Sprintf("%s-lb", cfg.Cluster.Name),
		Algorithm: "round_robin",
		Region:    cfg.DigitalOcean.Region,
		ForwardingRules: []godo.ForwardingRule{
			{
				EntryProtocol:  "http",
				EntryPort:      80,
				TargetProtocol: "http",
				TargetPort:     80,
				TlsPassthrough: false,
			},
			{
				EntryProtocol:  "https",
				EntryPort:      443,
				TargetProtocol: "https",
				TargetPort:     443,
				TlsPassthrough: true,
			},
		},
		HealthCheck: &godo.HealthCheck{
			Protocol:               "http",
			Port:                   80,
			Path:                   "/_mael_health_check",
			CheckIntervalSeconds:   3,
			ResponseTimeoutSeconds: 5,
			HealthyThreshold:       2,
			UnhealthyThreshold:     3,
		},
		StickySessions:      nil,
		Tags:                []string{dropletTag},
		RedirectHttpToHttps: true,
		EnableProxyProtocol: false,
	})
	if err != nil {
		return CreateClusterOut{}, fmt.Errorf("createCluster: unable to create load balancer: %v", err)
	}

	// create droplet firewall
	log.Info("digitalocean: creating droplet firewall")
	allIPs := []string{"0.0.0.0/0", "::/0"}
	_, _, err = d.client.Firewalls.Create(*d.ctx, &godo.FirewallRequest{
		Name: fmt.Sprintf("%s-fw-droplet", cfg.Cluster.Name),
		InboundRules: []godo.InboundRule{
			{
				Protocol:  "tcp",
				PortRange: "22",
				Sources:   &godo.Sources{Addresses: allIPs},
			},
			{
				Protocol:  "tcp",
				PortRange: "80",
				Sources:   &godo.Sources{Addresses: allIPs},
			},
			{
				Protocol:  "tcp",
				PortRange: "443",
				Sources:   &godo.Sources{Addresses: allIPs},
			},
		},
		OutboundRules: []godo.OutboundRule{
			{
				Protocol:     "tcp",
				PortRange:    "all",
				Destinations: &godo.Destinations{Addresses: allIPs},
			},
			{
				Protocol:     "udp",
				PortRange:    "all",
				Destinations: &godo.Destinations{Addresses: allIPs},
			},
		},
		Tags: []string{fmt.Sprintf("cluster:%s", cfg.Cluster.Name)},
	})
	if err != nil {
		return CreateClusterOut{}, fmt.Errorf("createCluster: unable to create droplet firewall: %v", err)
	}

	// create droplets
	log.Info(fmt.Sprintf("digitalocean: creating %d droplet(s)", cfg.Cluster.MinSize))
	dropletTags := []string{clusterTag, dropletTag}
	out := CreateClusterOut{ClusterName: cfg.Cluster.Name}
	now := time.Now().Format("200601021504")
	for i := 0; i < cfg.Cluster.MinSize; i++ {
		vm, err := d.CreateVM(cfg, CreateVMOptions{
			VMName: fmt.Sprintf("%s-%s-%d", cfg.Cluster.Name, now, i),
			Tags:   dropletTags,
		})
		if err != nil {
			return CreateClusterOut{}, err
		}
		if i == 0 {
			out.RootVM = vm
		}
	}

	return out, nil
}

func (d *DOAdapter) DestroyCluster(cfg config.Config) error {
	_, err := d.getDOCluster(cfg)
	if err == nil {
		// delete resources in cluster
		return fmt.Errorf("digitalocean: DestroyCluster not implemented")
	} else {
		return err
	}
}

func (d *DOAdapter) GetClusterInfo(cfg config.Config) (ClusterInfo, error) {
	cluster, err := d.getDOCluster(cfg)
	if err == nil {
		return cluster.ToClusterInfo(), nil
	} else {
		return ClusterInfo{}, err
	}
}

func (d *DOAdapter) getDOCluster(cfg config.Config) (*DOCluster, error) {
	return nil, nil
}

func (d *DOAdapter) CreateVM(cfg config.Config, opts CreateVMOptions) (VM, error) {
	log.Info(fmt.Sprintf("digitalocean: creating droplet: %s\n", opts.VMName))
	droplet, _, err := d.client.Droplets.Create(*d.ctx, &godo.DropletCreateRequest{
		Name:   opts.VMName,
		Region: cfg.DigitalOcean.Region,
		Size:   cfg.DigitalOcean.DropletSize,
		Image:  godo.DropletCreateImage{Slug: cfg.DigitalOcean.ImageSlug},
		SSHKeys: []godo.DropletCreateSSHKey{{
			Fingerprint: cfg.DigitalOcean.SSHFingerprint,
		}},
		Backups:           cfg.DigitalOcean.Backups,
		IPv6:              cfg.DigitalOcean.IPV6,
		PrivateNetworking: true,
		Monitoring:        true,
		UserData:          userData,
		Volumes:           nil,
		Tags:              opts.Tags,
	})
	if err != nil {
		return VM{}, fmt.Errorf("digitalocean: error creating droplet: %v", err)
	}

	droplet, err = d.waitForDropletIPAddrs(droplet.ID, time.Minute)
	publicIp, privateIp, err := getDropletIpAddrs(droplet)
	if err != nil {
		return VM{}, err
	}

	return VM{
		Id:            strconv.Itoa(droplet.ID),
		CreatedAt:     time.Now(),
		PublicIpAddr:  publicIp,
		PrivateIpAddr: privateIp,
	}, nil
}

func getDropletIpAddrs(droplet *godo.Droplet) (string, string, error) {
	publicIp, err := droplet.PublicIPv4()
	if err != nil {
		return "", "", fmt.Errorf("digitalocean: error getting PublicIPV4 for %d: %v",
			droplet.ID, err)
	}
	privateIp, err := droplet.PrivateIPv4()
	if err != nil {
		return "", "", fmt.Errorf("digitalocean: error getting PrivateIPv4 for %d: %v",
			droplet.ID, err)
	}
	return publicIp, privateIp, nil
}

func (d *DOAdapter) waitForDropletIPAddrs(dropletId int, timeout time.Duration) (*godo.Droplet, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		droplet, _, err := d.client.Droplets.Get(*d.ctx, dropletId)
		if err != nil {
			return nil, err
		}
		publicIp, privateIp, err := getDropletIpAddrs(droplet)
		if err != nil {
			return nil, err
		}
		if publicIp != "" || privateIp != "" {
			return droplet, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("digitalocean: timeout waiting for IP addresses for droplet %d", dropletId)
}

func (d *DOAdapter) DestroyVM(opts DestroyVMOptions) error {
	return nil
}

func (d *DOAdapter) ListVMs(opts ListVMsOptions) ([]VM, error) {
	return nil, nil
}

type TokenSource struct {
	AccessToken string
}

func (t *TokenSource) Token() (*oauth2.Token, error) {
	token := &oauth2.Token{
		AccessToken: t.AccessToken,
	}
	return token, nil
}

type DOCluster struct {
	ClusterName  string
	LoadBalancer *godo.LoadBalancer
	Droplets     []*godo.Droplet
}

func (c *DOCluster) ToClusterInfo() ClusterInfo {
	info := ClusterInfo{
		ClusterName: c.ClusterName,
		VMs:         make([]VM, 0),
	}

	if c.LoadBalancer != nil {
		info.LoadBalancer = &LoadBalancer{PublicIpAddr: c.LoadBalancer.IP}
	}

	for _, droplet := range c.Droplets {
		publicIp, privateIp, err := getDropletIpAddrs(droplet)
		if err != nil {
			log.Error("digitalocean: error getting droplet IP addresses", "err", err)
		}
		created, err := time.Parse(droplet.Created, "2006-01-02T15:04:05Z")
		if err != nil {
			log.Error("digitalocean: error parsing droplet created date", "err", err, "created", droplet.Created)
		}
		info.VMs = append(info.VMs, VM{
			Id:            strconv.Itoa(droplet.ID),
			CreatedAt:     created,
			PublicIpAddr:  publicIp,
			PrivateIpAddr: privateIp,
		})
	}

	return info
}

const userData string = `#!/bin/bash -ex

apt-get update
apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg2 \
    software-properties-common
curl -fsSL https://download.docker.com/linux/debian/gpg | apt-key add -

add-apt-repository \
   "deb [arch=amd64] https://download.docker.com/linux/debian \
   $(lsb_release -cs) \
   stable"

apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io

# install digital ocean metrics agent
# see: https://www.digitalocean.com/docs/monitoring/quickstart/
curl -sSL https://insights.nyc3.cdn.digitaloceanspaces.com/install.sh | bash

# install maelstrom
cd /usr/bin
curl -LO https://bitmech-west2.s3.amazonaws.com/maelstrom/latest/maelstromd
curl -LO https://bitmech-west2.s3.amazonaws.com/maelstrom/latest/maelctl
chmod 755 maelstromd maelctl

# create systemd unit
cat << 'EOF' > /etc/systemd/system/maelstromd.service
[Unit]
Description=maelstromd
After=docker.service

[Service]
TimeoutStartSec=0
Restart=always
RestartSec=5
ExecStartPre=/bin/mkdir -p /var/maelstrom
ExecStartPre=/bin/chmod 700 /var/maelstrom
ExecStart=/usr/bin/maelstromd -sqlDriver sqlite3 -sqlDSN 'file:/var/maelstrom/maelstrom.db?cache=shared&_journal_mode=MEMORY'

[Install]
WantedBy=multi-user.target
EOF

# start maelstromd
systemctl daemon-reload
systemctl enable maelstromd
systemctl start maelstromd
`
