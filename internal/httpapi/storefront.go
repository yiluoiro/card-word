package httpapi

import "strings"

// Brand cards are discovery links, not invented products, prices or stock.
type brandCard struct {
	Name, Query, Image string
}

var storefrontBrands = []brandCard{
	{"OpenAI", "OpenAI", "openai"},
	{"Google Play", "Google Play", "google-play"},
	{"Steam", "Steam", "steam"},
	{"PlayStation", "PlayStation", "playstation"},
	{"Amazon", "Amazon", "amazon"},
	{"iTunes", "iTunes", "itunes"},
}

func productBrandImage(name string) string {
	name = strings.ToLower(name)
	for _, brand := range storefrontBrands {
		if strings.Contains(name, strings.ToLower(brand.Name)) {
			return brand.Image
		}
	}
	return ""
}
