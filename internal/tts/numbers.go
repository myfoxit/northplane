package tts

import (
	"strings"
)

// NumberWords writes an integer out in words for the given language
// ("en", "de"); ok=false when the language has no writer (callers then
// leave the digits to the engine). Magnitudes up to 10^15.
func NumberWords(n int64, lang string) (string, bool) {
	switch langPrefix(lang) {
	case "en":
		return englishNumber(n), true
	case "de":
		return germanNumber(n), true
	}
	return "", false
}

// langPrefix returns the lower-case primary subtag of a BCP-47 tag.
func langPrefix(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// --- English -------------------------------------------------------------

var enOnes = []string{"zero", "one", "two", "three", "four", "five", "six", "seven",
	"eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen",
	"sixteen", "seventeen", "eighteen", "nineteen"}
var enTens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
var enScales = []struct {
	value int64
	name  string
}{
	{1_000_000_000_000_000, "quadrillion"},
	{1_000_000_000_000, "trillion"},
	{1_000_000_000, "billion"},
	{1_000_000, "million"},
	{1_000, "thousand"},
}

func englishNumber(n int64) string {
	if n < 0 {
		return "minus " + englishNumber(-n)
	}
	if n < 20 {
		return enOnes[n]
	}
	if n < 100 {
		s := enTens[n/10]
		if n%10 != 0 {
			s += "-" + enOnes[n%10]
		}
		return s
	}
	if n < 1000 {
		s := enOnes[n/100] + " hundred"
		if n%100 != 0 {
			s += " " + englishNumber(n%100)
		}
		return s
	}
	for _, sc := range enScales {
		if n >= sc.value {
			s := englishNumber(n/sc.value) + " " + sc.name
			if rest := n % sc.value; rest != 0 {
				s += " " + englishNumber(rest)
			}
			return s
		}
	}
	return ""
}

// --- German --------------------------------------------------------------

var deOnes = []string{"null", "eins", "zwei", "drei", "vier", "fünf", "sechs", "sieben",
	"acht", "neun", "zehn", "elf", "zwölf", "dreizehn", "vierzehn", "fünfzehn",
	"sechzehn", "siebzehn", "achtzehn", "neunzehn"}
var deTens = []string{"", "", "zwanzig", "dreißig", "vierzig", "fünfzig", "sechzig", "siebzig", "achtzig", "neunzig"}

// deUnit is the form used as a prefix ("ein-und-zwanzig", "ein-hundert").
func deUnit(n int64) string {
	if n == 1 {
		return "ein"
	}
	return deOnes[n]
}

func germanNumber(n int64) string {
	if n < 0 {
		return "minus " + germanNumber(-n)
	}
	if n < 20 {
		return deOnes[n]
	}
	if n < 100 {
		if n%10 == 0 {
			return deTens[n/10]
		}
		return deUnit(n%10) + "und" + deTens[n/10]
	}
	if n < 1000 {
		s := deUnit(n/100) + "hundert"
		if n%100 != 0 {
			s += germanNumber(n % 100)
		}
		return s
	}
	if n < 1_000_000 {
		s := germanNumber(n/1000) + "tausend"
		if n/1000 == 1 {
			s = "eintausend"
		}
		if n%1000 != 0 {
			s += germanNumber(n % 1000)
		}
		return s
	}
	type scale struct {
		value            int64
		singular, plural string
	}
	scales := []scale{
		{1_000_000_000_000_000, "Billiarde", "Billiarden"},
		{1_000_000_000_000, "Billion", "Billionen"},
		{1_000_000_000, "Milliarde", "Milliarden"},
		{1_000_000, "Million", "Millionen"},
	}
	for _, sc := range scales {
		if n >= sc.value {
			q := n / sc.value
			var s string
			if q == 1 {
				s = "eine " + sc.singular
			} else {
				s = germanNumber(q) + " " + sc.plural
			}
			if rest := n % sc.value; rest != 0 {
				s += " " + germanNumber(rest)
			}
			return s
		}
	}
	return ""
}
