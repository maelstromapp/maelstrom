package common

import (
	"bytes"
	"regexp"
	"strings"
)

var interpolateRE = regexp.MustCompile(`(?m)(\$?\${[^}]+})`)

func parseToken(tok string) (key string, defaultVal string, emptyToDefault bool) {
	// strip ${ and }
	tok = tok[2 : len(tok)-1]
	key = tok
	pos := strings.Index(tok, ":")
	if pos > -1 {
		key = tok[0:pos]
		if pos < len(tok)-1 {
			if tok[pos+1] == '-' {
				emptyToDefault = true
				pos++
			}
			if pos < len(tok)-1 {
				defaultVal = tok[pos+1:]
			}
		}
	}
	return
}

func InterpolateWithMap(input string, vars map[string]string) string {
	out := bytes.NewBufferString("")
	matches := interpolateRE.FindAllStringIndex(input, -1)
	pos := 0
	for _, m := range matches {
		tok := input[m[0]:m[1]]
		var val string
		var ok bool
		if strings.HasPrefix(tok, "$${") {
			val = tok[1:]
		} else {
			key, defaultVal, emptyToDefault := parseToken(tok)
			val, ok = vars[key]
			if !ok || (val == "" && emptyToDefault) {
				val = defaultVal
			}
		}
		out.WriteString(input[pos:m[0]])
		out.WriteString(val)
		pos = m[1]
	}
	out.WriteString(input[pos:])
	return out.String()
}

func StrTruncate(s string, maxlen int) string {
	if len(s) > maxlen {
		return s[0:maxlen]
	}
	return s
}

func TruncNodeId(id string) string {
	return StrTruncate(id, 14)
}

func GlobMatches(globOrStr string, target string) bool {
	if globOrStr == "*" || globOrStr == target {
		return true
	}

	if strings.HasPrefix(globOrStr, "*") && strings.HasSuffix(globOrStr, "*") {
		return strings.Contains(target, globOrStr[1:len(globOrStr)-1])
	}
	if strings.HasPrefix(globOrStr, "*") {
		return strings.HasSuffix(target, globOrStr[1:])
	}
	if strings.HasSuffix(globOrStr, "*") {
		return strings.HasPrefix(target, globOrStr[:len(globOrStr)-1])
	}
	return false
}
