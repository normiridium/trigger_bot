package app

import (
	"strings"
	"unicode"
)

type pronounFlags struct {
	any     bool
	none    bool
	he      bool
	she     bool
	it      bool
	neutral bool
	they    bool
}

type genderVariants struct {
	Male    string
	Female  string
	Neuter  string
	Plural  string
	Unknown string
}

func resolveGenderVariant(tag string, variants genderVariants) string {
	flags := detectPronounFlags(tag)
	if flags.they {
		return variants.Plural
	}
	if flags.he {
		return variants.Male
	}
	if flags.she {
		return variants.Female
	}
	if flags.it {
		return variants.Neuter
	}
	return variants.Unknown
}

func detectPronounFlags(raw string) pronounFlags {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return pronounFlags{}
	}
	tokens := splitPronounTokens(raw)
	flags := pronounFlags{}
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if tok == "any" || strings.HasPrefix(tok, "люб") {
			flags.any = true
		}
		if isPronounNoneToken(tok) {
			flags.none = true
		}
		if isPronounHeToken(tok) {
			flags.he = true
		}
		if isPronounSheToken(tok) {
			flags.she = true
		}
		if isPronounItToken(tok) {
			flags.it = true
		}
		if isPronounNeutralToken(tok) {
			flags.neutral = true
		}
		if isPronounTheyToken(tok) {
			flags.they = true
		}
	}
	return flags
}

func splitPronounTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isPronounNoneToken(tok string) bool {
	switch tok {
	case "0", "vo", "nul", "null", "no", "нет", "избег":
		return true
	default:
		return false
	}
}

func isPronounHeToken(tok string) bool {
	switch tok {
	case "1", "him", "boy", "mas", "его", "па", "му", "тот", "he", "он", "mal", "male", "man":
		return true
	default:
		if strings.HasPrefix(tok, "mas") {
			return true
		}
		return false
	}
}

func isPronounSheToken(tok string) bool {
	switch tok {
	case "2", "she", "her", "wom", "woman", "gir", "girl", "fem", "female", "она", "её", "ее", "де", "же", "та", "фем":
		return true
	default:
		if strings.HasPrefix(tok, "fem") || strings.HasPrefix(tok, "wom") || strings.HasPrefix(tok, "gir") {
			return true
		}
		return false
	}
}

func isPronounItToken(tok string) bool {
	switch tok {
	case "3", "it":
		return true
	default:
		return false
	}
}

func isPronounNeutralToken(tok string) bool {
	switch tok {
	case "4", "one", "neu", "оно", "то":
		return true
	default:
		if strings.HasPrefix(tok, "neu") {
			return true
		}
		return false
	}
}

func isPronounTheyToken(tok string) bool {
	switch tok {
	case "5", "the", "они", "их", "эти", "те":
		return true
	default:
		return false
	}
}
