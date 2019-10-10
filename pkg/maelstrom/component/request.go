package component

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"time"
)

type RequestInput struct {
	Resp          http.ResponseWriter
	Req           *http.Request
	Component     *v1.Component
	PublicGateway bool
	StartTime     time.Time
	Done          chan bool
}
