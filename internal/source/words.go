package source

// Words counts the identifier-like words of a source file: runs of ASCII
// letters, digits and underscores not starting with a digit, wherever they
// appear, comments and strings included. A name that appears nowhere but
// in its own declaration is not referenced anywhere, not even by a macro,
// an attribute string or a doc link.
func Words(src []byte) map[string]int {
	words := map[string]int{}
	start := -1
	for i := 0; i <= len(src); i++ {
		if i < len(src) && (isWordByte(src[i]) || (start >= 0 && isDigit(src[i]))) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words[string(src[start:i])]++
			start = -1
		}
		// a digit run is skipped whole, so "2x" is not the word "x"
		for i < len(src) && isDigit(src[i]) {
			i++
			for i < len(src) && (isWordByte(src[i]) || isDigit(src[i])) {
				i++
			}
		}
	}
	return words
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Capitalized reports whether a name starts with an upper-case ASCII
// letter: an exported Go name, a Rust enum variant or constant in a
// pattern. Words counts ASCII identifiers only, so a name starting
// otherwise is never one of them.
func Capitalized(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}
