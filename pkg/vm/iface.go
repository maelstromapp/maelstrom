package vm

import (
	"fmt"
	"gitlab.com/coopernurse/maelstrom/pkg/config"
	"time"
)

var NotFound = fmt.Errorf("not found")

type Adapter interface {
	Name() string
	CreateCluster(cfg config.Config, opts CreateClusterOptions) (CreateClusterOut, error)
	DestroyCluster(cfg config.Config) error
	GetClusterInfo(cfg config.Config) (ClusterInfo, error)
	CreateVM(cfg config.Config, opts CreateVMOptions) (VM, error)
	DestroyVM(opts DestroyVMOptions) error
	ListVMs(opts ListVMsOptions) ([]VM, error)
}

type VM struct {
	Id            string
	CreatedAt     time.Time
	PublicIpAddr  string
	PrivateIpAddr string
}

type LoadBalancer struct {
	PublicIpAddr string
	Hostname     string
}

type CreateClusterOptions struct {
	SSHPublicKeyFile  string
	SSHPrivateKeyFile string
}

type CreateClusterOut struct {
	ClusterName string
	RootVM      VM
}

type ClusterInfo struct {
	ClusterName  string
	LoadBalancer *LoadBalancer
	VMs          []VM
	Meta         map[string]string
}

type CreateVMOptions struct {
	VMName string
	Tags   []string
}

type DestroyVMOptions struct {
	Ids []string
}

type ListVMsOptions struct {
	ClusterName string
}
