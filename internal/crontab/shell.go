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
	pos         int  // byte offset of the token in the input
	needsTarget bool // redirection whose target is the next word
}

// word is a shell word and its byte offset in the command line.
type word struct {
	text string
	pos  int
}

// shellSplit is a small, forgiving POSIX-shell tokenizer. It is only used to
// find command names in crontab lines, not to execute anything.
func shellSplit(s string) []token {
	r := []rune(s)
	off := make([]int, len(r)+1) // byte offset of each rune
	for i, b := 0, 0; i < len(r); i++ {
		off[i] = b
		b += len(string(r[i]))
		off[i+1] = b
	}

	var (
		toks   []token
		cur    strings.Builder
		inWord bool
		wstart int
	)
	begin := func(i int) {
		if !inWord {
			inWord = true
			wstart = off[i]
		}
	}
	flush := func() {
		if inWord {
			toks = append(toks, token{text: cur.String(), kind: tokWord, pos: wstart})
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
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '#' && !inWord:
			flush()
			return toks
		case c == '\\':
			begin(i)
			if i+1 < len(r) {
				i++
				cur.WriteRune(r[i])
			}
		case c == '\'':
			begin(i)
			for i++; i < len(r) && r[i] != '\''; i++ {
				cur.WriteRune(r[i])
			}
		case c == '"':
			begin(i)
			for i++; i < len(r) && r[i] != '"'; i++ {
				if r[i] == '\\' && i+1 < len(r) && strings.ContainsRune("\"\\$`", r[i+1]) {
					i++
				}
				cur.WriteRune(r[i])
			}
		case c == '>' || c == '<':
			op, pos := "", off[i]
			if inWord && allDigits(cur.String()) {
				op, pos = cur.String(), wstart
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
			toks = append(toks, token{text: op, kind: tokRedir, pos: pos, needsTarget: !complete})
		case c == '&' && i+1 < len(r) && r[i+1] == '>':
			flush()
			pos := off[i]
			i++
			op := "&>"
			if i+1 < len(r) && r[i+1] == '>' {
				i++
				op += ">"
			}
			toks = append(toks, token{text: op, kind: tokRedir, pos: pos, needsTarget: true})
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			flush()
			pos := off[i]
			op := string(c)
			if i+1 < len(r) && (c == '&' || c == '|' || c == ';') && (r[i+1] == c || (c == '|' && r[i+1] == '&')) {
				i++
				op += string(r[i])
			}
			toks = append(toks, token{text: op, kind: tokSep, pos: pos})
		default:
			begin(i)
			cur.WriteRune(c)
		}
	}
	flush()
	return toks
}

// simpleCommands splits a command line into simple commands (lists of words),
// dropping separators and redirections.
func simpleCommands(s string) [][]word {
	var (
		out  [][]word
		cur  []word
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
			cur = append(cur, word{t.text, t.pos})
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func texts(ws []word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.text
	}
	return out
}
