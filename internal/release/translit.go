package release

import (
	"strings"
	"unicode"
)

// Ukrainian national transliteration (KMU 2010). Letters with a separate
// word-initial form are in translitStart.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "h", 'ґ': "g", 'д': "d", 'е': "e",
	'є': "ie", 'ж': "zh", 'з': "z", 'и': "y", 'і': "i", 'ї': "i", 'й': "i",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch",
	'ш': "sh", 'щ': "shch", 'ь': "", 'ю': "iu", 'я': "ia",
	// Russian letters that appear in names.
	'ы': "y", 'э': "e", 'ё': "io", 'ъ': "",
}

var translitStart = map[rune]string{'є': "ye", 'ї': "yi", 'й': "y", 'ю': "yu", 'я': "ya"}

// Translit transliterates Cyrillic to Latin and keeps every other rune.
func Translit(s string) string {
	var b strings.Builder
	prev := ' '
	for _, r := range s {
		lower := unicode.ToLower(r)
		out, ok := translit[lower]
		if !ok {
			b.WriteRune(r)
			prev = r
			continue
		}
		if start, ok := translitStart[lower]; ok && !unicode.IsLetter(prev) {
			out = start
		}
		if lower == 'г' && unicode.ToLower(prev) == 'з' {
			out = "gh"
		}
		if unicode.IsUpper(r) && out != "" {
			out = strings.ToUpper(out[:1]) + out[1:]
		}
		b.WriteString(out)
		prev = r
	}
	return b.String()
}
