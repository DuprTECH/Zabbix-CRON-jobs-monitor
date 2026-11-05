package crontab

import "strings"

type tokKind int

const (
	tokWord  tokKind = iota
	tokSep           // ; & && || | |& ( ) newline
	tokRedir         // > >> < 2> &> 2>&1 ...
)

type token struct {
	text        string
	kind        tokKind
	needsTarget bool // redirection whose target is the next word
}

// shellSplit is a small, forgiving POSIX-shell tokenizer. It is only used to
// find command names in crontab lines, not to execute anything.
func shellSplit(s string) []token {
	var (
		toks   []token
		cur    strings.Builder
		inWord bool
	)
	flush := func() {
		if inWord {
			toks = append(toks, token{text: cur.String(), kind: tokWord})
			cur.Reset()
			inWord = false
		}
	}
	allDigits := func(x string) bool {
		if x == "" {
			return false
		}
		for _, c := range x {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '#' && !inWord:
			flush()
			return toks
		case c == '\\':
			inWord = true
			if i+1 < len(r) {
				i++
				cur.WriteRune(r[i])
			}
		case c == '\'':
			inWord = true
			for i++; i < len(r) && r[i] != '\''; i++ {
				cur.WriteRune(r[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(r) && r[i] != '"'; i++ {
				if r[i] == '\\' && i+1 < len(r) && strings.ContainsRune("\"\\$`", r[i+1]) {
					i++
				}
				cur.WriteRune(r[i])
			}
		case c == '>' || c == '<':
			op := ""
			if inWord && allDigits(cur.String()) {
				op = cur.String()
				cur.Reset()
				inWord = false
			}
			flush()
			op += string(c)
			complete := false
			if i+1 < len(r) && (r[i+1] == '>' || (c == '<' && r[i+1] == '<')) {
				i++
				op += string(r[i])
			}
			if i+1 < len(r) && r[i+1] == '&' {
				i++
				op += "&"
				for i+1 < len(r) && ((r[i+1] >= '0' && r[i+1] <= '9') || r[i+1] == '-') {
					i++
					op += string(r[i])
					complete = true
				}
			}
			toks = append(toks, token{text: op, kind: tokRedir, needsTarget: !complete})
		case c == '&' && i+1 < len(r) && r[i+1] == '>':
			flush()
			i++
			op := "&>"
			if i+1 < len(r) && r[i+1] == '>' {
				i++
				op += ">"
			}
			toks = append(toks, token{text: op, kind: tokRedir, needsTarget: true})
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			flush()
			op := string(c)
			if i+1 < len(r) && (c == '&' || c == '|' || c == ';') && (r[i+1] == c || (c == '|' && r[i+1] == '&')) {
				i++
				op += string(r[i])
			}
			toks = append(toks, token{text: op, kind: tokSep})
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	flush()
	return toks
}

// simpleCommands splits a command line into simple commands (lists of words),
// dropping separators and redirections.
func simpleCommands(s string) [][]string {
	var (
		out  [][]string
		cur  []string
		skip bool
	)
	for _, t := range shellSplit(s) {
		switch t.kind {
		case tokSep:
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
		case tokRedir:
			skip = t.needsTarget
		default:
			if skip {
				skip = false
				continue
			}
			cur = append(cur, t.text)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
