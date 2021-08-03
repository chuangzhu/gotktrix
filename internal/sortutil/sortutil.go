package sortutil

import (
	"unicode"
	"unicode/utf8"
)

func popRune(str *string) rune {
	r, sz := utf8.DecodeRuneInString(*str)
	if sz == 0 {
		return utf8.RuneError
	}

	*str = (*str)[sz:]
	return r
}

// StrlessFold returns true if i < j case-insensitive. See StrcmpFold.
func StrlessFold(i, j string) bool {
	return StrcmpFold(i, j) == -1
}

// StrcmpFold compares 2 strings in a case-insensitive manner. If the string is
// prefixed with !, then it's put to last.
func StrcmpFold(i, j string) int {
	for {
		ir := popRune(&i)
		jr := popRune(&j)

		if ir == utf8.RuneError || jr == utf8.RuneError {
			if i == "" && j != "" {
				// len(i) < len(j)
				return -1
			}
			if i != "" && j == "" {
				// len(i) > len(j)
				return 1
			}
			return 0
		}

		if ir == '!' {
			return 1 // put last
		}

		if jr == '!' {
			return -1 // put last
		}

		if eq := compareRuneFold(ir, jr); eq != 0 {
			return eq
		}
	}
}

func compareRuneFold(i, j rune) int {
	if i == j {
		return 0
	}

	li := unicode.ToLower(i)
	lj := unicode.ToLower(j)

	if li != lj {
		if li < lj {
			return -1
		}
		return 1
	}

	if i < j {
		return -1
	}
	return 1
}
