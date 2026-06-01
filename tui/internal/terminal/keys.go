package terminal

import (
	"strings"
	"unicode/utf8"
)

type KeyType int

const (
	KeyUnknown KeyType = iota
	KeyRune
	KeyPaste
	KeyEnter
	KeyShiftEnter
	KeyBackspace
	KeyDelete
	KeyCtrlA
	KeyCtrlB
	KeyCtrlC
	KeyCtrlD
	KeyCtrlE
	KeyCtrlF
	KeyCtrlK
	KeyCtrlO
	KeyCtrlU
	KeyCtrlW
	KeyTab
	KeyEscape
	KeyArrowUp
	KeyArrowDown
	KeyArrowLeft
	KeyArrowRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
)

type Key struct {
	Type KeyType
	Rune rune
	Text string
}

func ParseKeys(data []byte) []Key {
	if len(data) == 0 {
		return nil
	}

	if key := parsePasteKey(data); key.Type != KeyUnknown {
		return []Key{key}
	}

	if key := parseEscapeKey(data); key.Type != KeyUnknown {
		return []Key{key}
	}
	if data[0] == Escape[0] && len(data) > 1 {
		return []Key{{Type: KeyUnknown}}
	}

	if len(data) == 1 {
		switch data[0] {
		case 1:
			return []Key{{Type: KeyCtrlA}}
		case 2:
			return []Key{{Type: KeyCtrlB}}
		case 3:
			return []Key{{Type: KeyCtrlC}}
		case 4:
			return []Key{{Type: KeyCtrlD}}
		case 5:
			return []Key{{Type: KeyCtrlE}}
		case 6:
			return []Key{{Type: KeyCtrlF}}
		case 11:
			return []Key{{Type: KeyCtrlK}}
		case 15:
			return []Key{{Type: KeyCtrlO}}
		case 21:
			return []Key{{Type: KeyCtrlU}}
		case 23:
			return []Key{{Type: KeyCtrlW}}
		case 9:
			return []Key{{Type: KeyTab}}
		case 13, 10:
			return []Key{{Type: KeyEnter}}
		case 27:
			return []Key{{Type: KeyEscape}}
		case 127, 8:
			return []Key{{Type: KeyBackspace}}
		}
	}

	r, size := utf8.DecodeRune(data)
	if r == utf8.RuneError {
		return []Key{{Type: KeyUnknown}}
	}

	if size == len(data) {
		return []Key{{Type: KeyRune, Rune: r}}
	}

	return []Key{
		{
			Type: KeyPaste,
			Text: normalizePastedText(string(data)),
		},
	}
}

func parsePasteKey(data []byte) Key {
	text := string(data)
	if !strings.HasPrefix(text, PasteStart) || !strings.Contains(text, PasteEnd) {
		return Key{Type: KeyUnknown}
	}
	text = strings.TrimPrefix(text, PasteStart)
	if index := strings.Index(text, PasteEnd); index >= 0 {
		text = text[:index]
	}
	return Key{Type: KeyPaste, Text: normalizePastedText(text)}
}

func normalizePastedText(text string) string {
	text = strings.ReplaceAll(text, CRLF, LineFeed)
	text = strings.ReplaceAll(text, CarriageReturn, LineFeed)
	return text
}

func parseEscapeKey(data []byte) Key {
	s := string(data)
	switch s {
	case KeyEscapeCSIu, KeyCtrlEscapeCSIu, KeyCtrlEscapeModifyOther:
		return Key{Type: KeyEscape}
	case KeyShiftEnterCSIu, KeyShiftEnterModifyOther:
		return Key{Type: KeyShiftEnter}
	case KeyCtrlACSIu, KeyCtrlAModifyOther:
		return Key{Type: KeyCtrlA}
	case KeyCtrlBCSIu, KeyCtrlBModifyOther:
		return Key{Type: KeyCtrlB}
	case KeyCtrlCCSIu, KeyCtrlCModifyOther:
		return Key{Type: KeyCtrlC}
	case KeyCtrlDCSIu, KeyCtrlDModifyOther:
		return Key{Type: KeyCtrlD}
	case KeyCtrlECSIu, KeyCtrlEModifyOther:
		return Key{Type: KeyCtrlE}
	case KeyCtrlFCSIu, KeyCtrlFModifyOther:
		return Key{Type: KeyCtrlF}
	case KeyCtrlKCSIu, KeyCtrlKModifyOther:
		return Key{Type: KeyCtrlK}
	case KeyCtrlOCSIu, KeyCtrlOModifyOther:
		return Key{Type: KeyCtrlO}
	case KeyCtrlUCSIu, KeyCtrlUModifyOther:
		return Key{Type: KeyCtrlU}
	case KeyCtrlWCSIu, KeyCtrlWModifyOther:
		return Key{Type: KeyCtrlW}
	case SequenceArrowUp:
		return Key{Type: KeyArrowUp}
	case SequenceArrowDown:
		return Key{Type: KeyArrowDown}
	case SequenceArrowRight:
		return Key{Type: KeyArrowRight}
	case SequenceArrowLeft:
		return Key{Type: KeyArrowLeft}
	case SequenceHome, SequenceHomeTilde, SequenceHomeSS3:
		return Key{Type: KeyHome}
	case SequenceEnd, SequenceEndTilde, SequenceEndSS3:
		return Key{Type: KeyEnd}
	case SequenceDelete:
		return Key{Type: KeyDelete}
	case SequencePageUp:
		return Key{Type: KeyPageUp}
	case SequencePageDown:
		return Key{Type: KeyPageDown}
	default:
		return Key{Type: KeyUnknown}
	}
}
