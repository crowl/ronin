package codeindex

import "strings"

// ignoreRules is ordered from ancestor to descendant and first to last line.
// Paths are workspace-relative with '/' separators on every platform. The
// walker must check/prune directories before children: a negation cannot restore
// a file beneath an excluded directory, whose .gitignore is never read.
type ignoreRules []ignoreRule

type ignoreRule struct {
	base      string
	basename  bool
	directory bool
	include   bool
	glob      []ignoreToken
}

type ignoreToken struct {
	kind    byte // literal, ?, *, **, **/, or character class
	literal byte
	class   [256]bool
}

const (
	ignoreLiteral byte = iota
	ignoreOne
	ignoreStar
	ignoreTree
	ignoreDirectories
	ignoreClass
)

func (rules ignoreRules) appendFile(base string, data []byte) ignoreRules {
	// Never overwrite a parent's backing array when descending into siblings.
	result := append(ignoreRules(nil), rules...)
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		// Only unescaped trailing spaces are discarded. Tabs are literal.
		for strings.HasSuffix(line, " ") {
			slashes := 0
			for j := len(line) - 2; j >= 0 && line[j] == '\\'; j-- {
				slashes++
			}
			if slashes%2 == 1 {
				break
			}
			line = line[:len(line)-1]
		}
		if line == "" || line[0] == '#' {
			continue
		}
		rule := ignoreRule{base: base}
		if line[0] == '!' {
			rule.include = true
			line = line[1:]
		}
		if line == "" {
			continue
		}
		rule.directory = strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		rule.basename = !strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		var ok bool
		rule.glob, ok = compileIgnoreGlob(line)
		// Malformed patterns never match, as with Git's wildmatch.
		if ok {
			result = append(result, rule)
		}
	}
	return result
}

func (rules ignoreRules) ignored(name string, directory bool) bool {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := &rules[i]
		if rule.directory && !directory {
			continue
		}
		relative := name
		if rule.base != "." {
			var ok bool
			relative, ok = strings.CutPrefix(name, rule.base+"/")
			if !ok {
				continue
			}
		}
		if rule.basename {
			if j := strings.LastIndexByte(relative, '/'); j >= 0 {
				relative = relative[j+1:]
			}
		}
		if matchIgnoreGlob(rule.glob, relative) {
			return !rule.include
		}
	}
	return false
}

func compileIgnoreGlob(pattern string) ([]ignoreToken, bool) {
	var tokens []ignoreToken
	for i := 0; i < len(pattern); {
		c := pattern[i]
		switch c {
		case '\\':
			i++
			if i == len(pattern) {
				return nil, false
			}
			tokens = append(tokens, ignoreToken{kind: ignoreLiteral, literal: pattern[i]})
			i++
		case '?':
			tokens = append(tokens, ignoreToken{kind: ignoreOne})
			i++
		case '*':
			start := i
			for i < len(pattern) && pattern[i] == '*' {
				i++
			}
			kind := ignoreStar
			if i-start >= 2 && (start == 0 || pattern[start-1] == '/') && (i == len(pattern) || pattern[i] == '/') {
				kind = ignoreTree
				if i < len(pattern) {
					kind = ignoreDirectories
					i++
				}
			}
			tokens = append(tokens, ignoreToken{kind: kind})
		case '[':
			token, next, ok := compileIgnoreClass(pattern, i+1)
			if !ok {
				return nil, false
			}
			tokens = append(tokens, token)
			i = next
		default:
			tokens = append(tokens, ignoreToken{kind: ignoreLiteral, literal: c})
			i++
		}
	}
	return tokens, true
}

// Git wildmatch operates on bytes, not Unicode code points. Keeping that
// behavior also makes '?' and bracket classes independent of the host OS.
// Dynamic programming bounds matching to O(pattern * path) time and O(path)
// memory, avoiding exponential backtracking on repository-controlled patterns.
func matchIgnoreGlob(tokens []ignoreToken, name string) bool {
	previous := make([]bool, len(name)+1)
	next := make([]bool, len(name)+1)
	previous[0] = true
	for _, token := range tokens {
		clear(next)
		switch token.kind {
		case ignoreStar, ignoreTree:
			next[0] = previous[0]
			for j := 1; j <= len(name); j++ {
				next[j] = previous[j] || next[j-1] && (token.kind == ignoreTree || name[j-1] != '/')
			}
		case ignoreDirectories:
			reachable := false
			for j := 0; j <= len(name); j++ {
				next[j] = previous[j] || (j > 0 && name[j-1] == '/' && reachable)
				reachable = reachable || previous[j]
			}
		default:
			for j := 1; j <= len(name); j++ {
				c := name[j-1]
				matches := token.kind == ignoreLiteral && c == token.literal || token.kind == ignoreOne && c != '/' || token.kind == ignoreClass && c != '/' && token.class[c]
				next[j] = previous[j-1] && matches
			}
		}
		previous, next = next, previous
	}
	return previous[len(name)]
}

func compileIgnoreClass(pattern string, i int) (ignoreToken, int, bool) {
	token := ignoreToken{kind: ignoreClass}
	negate := false
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		negate = true
		i++
	}
	first := true
	for i < len(pattern) {
		if pattern[i] == ']' && !first {
			if negate {
				for c := range token.class {
					token.class[c] = !token.class[c]
				}
			}
			return token, i + 1, true
		}
		first = false
		if strings.HasPrefix(pattern[i:], "[:") {
			end := strings.Index(pattern[i+2:], ":]")
			if end < 0 {
				return token, 0, false
			}
			class := pattern[i+2 : i+2+end]
			// POSIX classes use the ASCII/C-locale definitions, like Git wildmatch.
			for c := 0; c < 256; c++ {
				match, ok := ignorePOSIXClass(class, byte(c))
				if !ok {
					return token, 0, false
				}
				token.class[c] = token.class[c] || match
			}
			i += end + 4
			continue
		}
		c := pattern[i]
		i++
		if c == '\\' {
			if i == len(pattern) {
				return token, 0, false
			}
			c = pattern[i]
			i++
		}
		if i+1 < len(pattern) && pattern[i] == '-' && pattern[i+1] != ']' {
			i++
			last := pattern[i]
			i++
			if last == '\\' {
				if i == len(pattern) {
					return token, 0, false
				}
				last = pattern[i]
				i++
			}
			for n := int(c); n <= int(last); n++ {
				token.class[n] = true
			}
		} else {
			token.class[c] = true
		}
	}
	return token, 0, false
}

func ignorePOSIXClass(class string, c byte) (bool, bool) {
	lower := c >= 'a' && c <= 'z'
	upper := c >= 'A' && c <= 'Z'
	digit := c >= '0' && c <= '9'
	switch class {
	case "alnum":
		return lower || upper || digit, true
	case "alpha":
		return lower || upper, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 32 || c == 127, true
	case "digit":
		return digit, true
	case "graph":
		return c > 32 && c < 127, true
	case "lower":
		return lower, true
	case "print":
		return c >= 32 && c < 127, true
	case "punct":
		return c > 32 && c < 127 && !lower && !upper && !digit, true
	case "space":
		return c == ' ' || (c >= '\t' && c <= '\r'), true
	case "upper":
		return upper, true
	case "xdigit":
		return digit || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'), true
	default:
		return false, false
	}
}
