package web

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// languageCountry is the country a bare language code suggests when the
// browser sent no region of its own.
//
// This is a weak signal and wrong for anyone living outside the country of the
// language they read in — which is exactly why the map pin it produces is
// shown at country level and never saved. It positions a starting view; only a
// pin somebody actually placed becomes data.
var languageCountry = map[string]string{
	"fr": "FR",
	"de": "DE",
	"es": "ES",
	"it": "IT",
	"nl": "NL",
	"pt": "PT",
	"en": "GB",
}

// CountryFromRequest guesses a country for the map's starting view, preferring
// the region the browser actually sent.
func CountryFromRequest(r *http.Request) string {
	header := r.Header.Get("Accept-Language")
	if header == "" {
		return ""
	}

	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return ""
	}
	for _, tag := range tags {
		if region, confidence := tag.Region(); confidence >= language.High {
			return region.String()
		}
	}
	// No region was offered: fall back to what the language alone suggests.
	for _, tag := range tags {
		base, _ := tag.Base()
		if country, ok := languageCountry[strings.ToLower(base.String())]; ok {
			return country
		}
	}
	return ""
}
