package component

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

type infoRequest struct {
	resp chan InfoResponse
}

type InfoResponse struct {
	Info    []v1.ComponentInfo
	Version int64
}
