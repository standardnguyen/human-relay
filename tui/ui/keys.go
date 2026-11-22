package ui

import "unicode/utf8"

// KeyType names a non-character key or says the key is a character.
type KeyType int

// Key types.
const (
	KeyRune KeyType = iota
	KeyEnter
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyTab
	KeyBacktab
	KeyBackspace
	KeyDelete
	KeyCtrlC
	KeyCtrlU
	KeyCtrlL
	KeyPasteStart
	KeyPasteEnd
)

// Key is one key press.
type Key struct {
	Type KeyType
	Rune rune // set for KeyRune
}

// ParseKeys turns terminal input bytes into keys. It returns the bytes it
// could not yet decide on (a split escape sequence or UTF-8 character) so the
// caller can append the next read. With final set, nothing is held back: a
// lone ESC is the Esc key and a truncated character is dropped.
// Unrecognised escape sequences are swallowed whole, so stray terminal replies
// never turn into key presses.
func ParseKeys(b []byte, final bool) (keys []Key, rest []byte) {
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(b) {
				if final {
					keys = append(keys, Key{Type: KeyEsc})
					i++
					continue
				}
				return keys, b[i:]
			}
			switch b[i+1] {
			case '[':
				j := i + 2
				for j < len(b) && b[j] >= 0x30 && b[j] <= 0x3f {
					j++
				}
				for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
					j++
				}
				if j >= len(b) {
					if final {
						return keys, nil
					}
					return keys, b[i:]
				}
				if b[j] < 0x40 || b[j] > 0x7e { // malformed: drop what we saw
					i = j + 1
					continue
				}
				if k, ok := csiKey(b[i+2:j], b[j]); ok {
					keys = append(keys, k)
				}
				i = j + 1
			case 'O':
				if i+2 >= len(b) {
					if final {
						return keys, nil
					}
					return keys, b[i:]
				}
				if k, ok := csiKey(nil, b[i+2]); ok {
					keys = append(keys, k)
				}
				i += 3
			case 0x1b:
				keys = append(keys, Key{Type: KeyEsc})
				i++
			default:
				// ESC followed at once by a character is an Alt chord (or a
				// sequence this parser does not know). It becomes a plain Esc
				// and the character is dropped, so Alt+a can never act as "a".
				_, n := utf8.DecodeRune(b[i+1:])
				if n == 1 && b[i+1] >= 0x80 && !final && !utf8.FullRune(b[i+1:]) {
					return keys, b[i:]
				}
				keys = append(keys, Key{Type: KeyEsc})
				i += 1 + n
			}
		case c == '\r' || c == '\n':
			keys = append(keys, Key{Type: KeyEnter})
			i++
		case c == '\t':
			keys = append(keys, Key{Type: KeyTab})
			i++
		case c == 0x7f || c == 0x08:
			keys = append(keys, Key{Type: KeyBackspace})
			i++
		case c == 0x03:
			keys = append(keys, Key{Type: KeyCtrlC})
			i++
		case c == 0x15:
			keys = append(keys, Key{Type: KeyCtrlU})
			i++
		case c == 0x0c:
			keys = append(keys, Key{Type: KeyCtrlL})
			i++
		case c < 0x20:
			i++
		case c < 0x80:
			keys = append(keys, Key{Type: KeyRune, Rune: rune(c)})
			i++
		default:
			if !final && !utf8.FullRune(b[i:]) {
				return keys, b[i:]
			}
			r, n := utf8.DecodeRune(b[i:])
			if r != utf8.RuneError || n > 1 {
				keys = append(keys, Key{Type: KeyRune, Rune: r})
			}
			i += n
		}
	}
	return keys, nil
}

func csiKey(params []byte, final byte) (Key, bool) {
	switch final {
	case 'A':
		return Key{Type: KeyUp}, true
	case 'B':
		return Key{Type: KeyDown}, true
	case 'C':
		return Key{Type: KeyRight}, true
	case 'D':
		return Key{Type: KeyLeft}, true
	case 'H':
		return Key{Type: KeyHome}, true
	case 'F':
		return Key{Type: KeyEnd}, true
	case 'Z':
		return Key{Type: KeyBacktab}, true
	case '~':
		switch string(params) {
		case "200":
			return Key{Type: KeyPasteStart}, true
		case "201":
			return Key{Type: KeyPasteEnd}, true
		case "1", "7":
			return Key{Type: KeyHome}, true
		case "4", "8":
			return Key{Type: KeyEnd}, true
		case "3":
			return Key{Type: KeyDelete}, true
		case "5":
			return Key{Type: KeyPgUp}, true
		case "6":
			return Key{Type: KeyPgDn}, true
		}
	}
	return Key{}, false
}
