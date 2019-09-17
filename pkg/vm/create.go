package vm

import (
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/config"
)

func CreateCluster(cfg config.Config, opts CreateClusterOptions, adapter Adapter) (CreateClusterOut, error) {
	out := CreateClusterOut{ClusterName: cfg.Cluster.Name}

	// - create ssh key pair

	err := common.MakeSSHKeyPair(opts.SSHPublicKeyFile, opts.SSHPrivateKeyFile, 2048)
	if err != nil {
		return out, err
	}

	// - Create CA key pair (for cluster node<->node communication)
	// - Cloud:
	//     - register ssh key
	//     - Create firewall
	//     - Create load balancer
	//     - Create first node
	return adapter.CreateCluster(cfg, opts)

	// - copy files to node:
	//     - ssh private key
	//     - Maelstrom binaries
	//     - CA key pair
	//     - Config file
	//     - systemd unit file
	// - register and start maelstromd service
}
