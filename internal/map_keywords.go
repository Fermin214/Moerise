package moerise

import (
	"slices"
	"strings"
	"unicode"
)

func mapKeywords(content string) []string {
	stop := "的了是在和与也就都而或把被让这那一个我们你它有不着地得为及到很更"
	englishStop := " the and for with that this from have will your you are was were has its into then than not but "
	terms := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) || unicode.Is(unicode.Han, r) && strings.ContainsRune(stop, r)
	}) {
		runes := []rune(word)
		if len(runes) < 2 {
			continue
		}
		if !unicode.Is(unicode.Han, runes[0]) {
			if len(runes) >= 3 && len(runes) <= 24 && !strings.Contains(englishStop, " "+word+" ") {
				terms[word] = true
			}
		} else if len(runes) <= 6 {
			terms[word] = true
		} else {
			for length := 2; length <= 4; length++ {
				for i := 0; i+length <= len(runes); i++ {
					terms[string(runes[i:i+length])] = true
				}
			}
		}
		if len(terms) >= 128 {
			break
		}
	}
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	slices.Sort(result)
	return result
}
