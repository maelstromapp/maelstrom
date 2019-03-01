package common

import (
	"fmt"
	"strings"
)

const PingLogMsg = "PING"

type LogMsg struct {
	Component string
	Stream    string
	Data      string
}

func (m LogMsg) Format() string {
	return fmt.Sprintf("[%s]\t%s", m.Component, strings.TrimSpace(m.Data))
}
