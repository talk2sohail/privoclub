package policy

import "errors"

// Glob patterns for rules. Unlike path.Match, '*' matches any run of
// characters including '/', because upstream tool names may contain '/' and a
// rule like "deny *" must cover them.
//
//	*      any run of characters (including none)
//	?      any single character
//	[abc]  one of the listed characters; [a-z] ranges; [!abc] negation
//	\x     the literal character x

var errBadPattern = errors.New("policy: malformed pattern")

// globMatch reports whether name matches pattern.
func globMatch(pattern, name string) (bool, error) {
	if err := checkPattern(pattern); err != nil {
		return false, err
	}
	return match([]rune(pattern), []rune(name)), nil
}

func checkPattern(p string) error {
	r := []rune(p)
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '\\':
			if i+1 >= len(r) {
				return errBadPattern
			}
			i++
		case '[':
			end := classEnd(r, i)
			if end < 0 {
				return errBadPattern
			}
			i = end
		}
	}
	return nil
}

// classEnd returns the index of the ']' closing the class that opens at i.
func classEnd(r []rune, i int) int {
	j := i + 1
	if j < len(r) && r[j] == '!' {
		j++
	}
	if j < len(r) && r[j] == ']' { // a leading ']' is literal
		j++
	}
	for ; j < len(r); j++ {
		if r[j] == '\\' {
			j++
			continue
		}
		if r[j] == ']' {
			return j
		}
	}
	return -1
}

func match(p, s []rune) bool {
	// Iterative matcher with single-star backtracking: linear in practice and
	// never exponential.
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		if pi < len(p) {
			switch p[pi] {
			case '*':
				star, mark = pi, si
				pi++
				continue
			case '?':
				pi++
				si++
				continue
			case '[':
				end := classEnd(p, pi)
				if inClass(p[pi+1:end], s[si]) {
					pi = end + 1
					si++
					continue
				}
			case '\\':
				if p[pi+1] == s[si] {
					pi += 2
					si++
					continue
				}
			default:
				if p[pi] == s[si] {
					pi++
					si++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		pi = star + 1
		mark++
		si = mark
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func inClass(class []rune, c rune) bool {
	negate := len(class) > 0 && class[0] == '!'
	if negate {
		class = class[1:]
	}
	found := false
	for i := 0; i < len(class); i++ {
		lo := class[i]
		if lo == '\\' && i+1 < len(class) {
			i++
			lo = class[i]
		}
		hi := lo
		if i+2 < len(class) && class[i+1] == '-' {
			hi = class[i+2]
			i += 2
		}
		if lo <= c && c <= hi {
			found = true
		}
	}
	return found != negate
}
