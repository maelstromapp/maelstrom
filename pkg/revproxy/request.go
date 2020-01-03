package revproxy

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"time"
)

func NewRequest(req *http.Request, rw http.ResponseWriter, comp *v1.Component, preferLocal bool) *Request {
	return &Request{
		Req:         req,
		Rw:          rw,
		Component:   comp,
		StartTime:   time.Now(),
		PreferLocal: preferLocal,
		Done:        make(chan bool, 1),
	}
}

type Request struct {
	Req         *http.Request
	Rw          http.ResponseWriter
	Component   *v1.Component
	StartTime   time.Time
	PreferLocal bool
	Done        chan bool
}
