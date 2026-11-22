package safety

import "unicode"

// ScriptOf names the Unicode script of r (for example "Cyrillic" for U+0430),
// which is what tells a homoglyph from a harmless accented letter.
func ScriptOf(r rune) string {
	for name, table := range unicode.Scripts {
		if unicode.Is(table, r) {
			return name
		}
	}
	return ""
}
