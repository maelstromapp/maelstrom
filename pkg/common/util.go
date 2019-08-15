package common

import (
	"bytes"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

func ToIntOrDefault(s string, defaultVal int) int {
	v, err := strconv.Atoi(s)
	if err == nil {
		return v
	}
	return defaultVal
}

func CheckClose(c io.Closer, err *error) {
	cerr := c.Close()
	if *err == nil {
		*err = cerr
	}
}

func EnvVarMap() map[string]string {
	return ParseEnvVarMap(os.Environ())
}

func ParseEnvVarMap(nvpairs []string) map[string]string {
	envVars := make(map[string]string)
	for _, s := range nvpairs {
		pos := strings.Index(s, "=")
		if pos > -1 {
			key := s[0:pos]
			val := s[pos+1:]
			envVars[key] = val
		}
	}
	return envVars
}

func SortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0)
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// dropCR drops a terminal \r from the data.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

// From: https://stackoverflow.com/questions/37530451/golang-bufio-read-multiline-until-crlf-r-n-delimiter
func ScanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.Index(data, []byte{'\r', '\n'}); i >= 0 {
		// We have a full newline-terminated line.
		return i + 2, dropCR(data[0:i]), nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), dropCR(data), nil
	}
	// Request more data.
	return 0, nil, nil
}

// Get preferred outbound IP of this machine
// From: https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go
func GetOutboundIP() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return net.IP{}, err
	}
	err = conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP, err
}
