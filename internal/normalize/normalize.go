// Package normalize turns the free text of a Uruguayan property listing into the
// canonical fields we can rank on.
//
// Deliberately regex + dictionaries, no LLM. The vocabulary of these listings is
// small and repetitive; a model per listing would be slower, costlier and less
// predictable. An LLM earns its place later, only for the genuinely ambiguous ones.
package normalize

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

// --- amenity vocabularies -------------------------------------------------
//
// These are written against how the ads actually read, not against correct
// Spanish. "loza radiante" (with z) is a very common misspelling in Uruguayan
// listings and missing it would drop real matches for the feature the buyer cares
// most about.

var radiantPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)lo[sz]a\s+radiante`),
	regexp.MustCompile(`(?i)piso\s+radiante`),
	regexp.MustCompile(`(?i)calefacci[oó]n\s+radiante`),
	regexp.MustCompile(`(?i)calefacci[oó]n\s+por\s+lo[sz]a`),
	regexp.MustCompile(`(?i)radiante\s+sectorizad`),
}

var heatingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)calefacci[oó]n`),
	regexp.MustCompile(`(?i)estufa\s+a\s+le[ñn]a`),
	regexp.MustCompile(`(?i)aire\s+acondicionado\s+fr[ií]o\s*[/-]?\s*calor`),
	regexp.MustCompile(`(?i)losa\s+radiante`),
	regexp.MustCompile(`(?i)caldera`),
}

var serviceRoomPatterns = []*regexp.Regexp{
	// Spec-table form used by part of the family: "Dep. Servicio Si". More reliable
	// than the prose, and invisible to a pattern that only knows the long spelling.
	regexp.MustCompile(`(?i)dep\.?\s*(?:de\s+)?servicio\s*:?\s*s[ií]\b`),
	regexp.MustCompile(`(?i)dependencia\s+de\s+servicio`),
	regexp.MustCompile(`(?i)\bdependencias?\b`),
	regexp.MustCompile(`(?i)(dormitorio|cuarto|habitaci[oó]n)\s+de\s+servicio`),
	regexp.MustCompile(`(?i)servicio\s+completo`),
	regexp.MustCompile(`(?i)cuarto\s+de\s+servicio`),
}

var serviceBathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ba[ñn]o\s+servicio\s*:?\s*s[ií]\b`),
	regexp.MustCompile(`(?i)ba[ñn]o\s+de\s+servicio`),
	regexp.MustCompile(`(?i)servicio\s+con\s+ba[ñn]o`),
	regexp.MustCompile(`(?i)dependencia\s+con\s+ba[ñn]o`),
}

var poolPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)piscina`),
	regexp.MustCompile(`(?i)\bpileta\b`),
}

var bbqPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)parrillero`),
	regexp.MustCompile(`(?i)\bbarbacoa\b`),
	regexp.MustCompile(`(?i)\bparrilla\b`),
}

var garagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bgarage?\b`),
	regexp.MustCompile(`(?i)\bcochera`),
}

// negation catches "sin piscina", "no tiene dependencia" — cheap, but it stops the
// most common way a keyword match says the opposite of what the ad means.
var negation = regexp.MustCompile(`(?i)\b(sin|no\s+tiene|no\s+posee|no\s+cuenta\s+con)\s+$`)

// HasFeature reports whether the text mentions a feature, and distinguishes
// "not mentioned" (nil) from "mentioned as absent" (false).
func HasFeature(text string, patterns []*regexp.Regexp) *bool {
	for _, re := range patterns {
		loc := re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		// Look at the ~20 characters before the match for a negation.
		start := max(0, loc[0]-20)
		if negation.MatchString(text[start:loc[0]]) {
			return model.Bool(false)
		}
		return model.Bool(true)
	}
	return nil
}

func RadiantHeating(text string) *bool { return HasFeature(text, radiantPatterns) }
func ServiceRoom(text string) *bool    { return HasFeature(text, serviceRoomPatterns) }
func ServiceBath(text string) *bool    { return HasFeature(text, serviceBathPatterns) }
func Pool(text string) *bool           { return HasFeature(text, poolPatterns) }
func BBQ(text string) *bool            { return HasFeature(text, bbqPatterns) }
func Garage(text string) *bool         { return HasFeature(text, garagePatterns) }

// Heating is true whenever any heating is mentioned; radiant implies it.
func Heating(text string) *bool {
	if r := RadiantHeating(text); r != nil && *r {
		return model.Bool(true)
	}
	return HasFeature(text, heatingPatterns)
}

// --- room counts ----------------------------------------------------------
//
// The family prints these both ways depending on the site's template:
//
//	"Dorms.: 3   Baños: 2"      (label first)
//	"3 Dormitorios  1 Baños"    (number first)
//
// so both orders are tried, label-first winning because it is the explicit form.

// Order matters. "2 Dormitorios 1 Baños" is a real string from these sites: a
// label pattern without the colon would read the 1 that belongs to Baños. So the
// colon form is tried first (unambiguous), then number-before-label, and the
// colonless label form last.
var (
	reBedroomsColon = regexp.MustCompile(`(?i)(?:dormitorios?|dorms?\.?|habitaciones?)\s*:\s*(\d{1,2})\b`)
	reBedroomsCount = regexp.MustCompile(`(?i)\b(\d{1,2})\s*(?:dormitorios?|dorms?\b|habitaciones?)`)
	reBedroomsLoose = regexp.MustCompile(`(?i)(?:dormitorios?|dorms?\.?|habitaciones?)\s+(\d{1,2})\b`)

	reBathsColon = regexp.MustCompile(`(?i)ba[ñn]os?\s*:\s*(\d{1,2})\b`)
	reBathsCount = regexp.MustCompile(`(?i)\b(\d{1,2})\s*ba[ñn]os?`)
	reBathsLoose = regexp.MustCompile(`(?i)ba[ñn]os?\s+(\d{1,2})\b`)

	reSuitesColon = regexp.MustCompile(`(?i)suites?\s*:\s*(\d{1,2})\b`)
	reSuitesCount = regexp.MustCompile(`(?i)\b(\d{1,2})\s*suites?`)

	reToilette = regexp.MustCompile(`(?i)\b(\d{1,2})?\s*(?:toilettes?|toilets?)\b`)
)

// Bedrooms extracts the bedroom count, or nil when the text does not say.
func Bedrooms(text string) *int { return firstCount(text, reBedroomsColon, reBedroomsCount, reBedroomsLoose) }

// Bathrooms extracts the bathroom count. Toilettes are counted separately: a
// "2 y toilette" listing has 2 bathrooms, not 3.
func Bathrooms(text string) *int { return firstCount(text, reBathsColon, reBathsCount, reBathsLoose) }

func Suites(text string) *int { return firstCount(text, reSuitesColon, reSuitesCount) }

// Toilettes counts half-baths. A bare "toilette" with no number means one.
func Toilettes(text string) *int {
	m := reToilette.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	if m[1] == "" {
		return model.Int(1)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	return model.Int(n)
}

func firstCount(text string, res ...*regexp.Regexp) *int {
	for _, re := range res {
		if m := re.FindStringSubmatch(text); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil && n >= 0 && n <= 30 {
				return model.Int(n)
			}
		}
	}
	return nil
}

// --- surfaces -------------------------------------------------------------

var (
	reBuilt = regexp.MustCompile(`(?i)(?:sup(?:erficie)?\.?\s*(?:edif(?:icada)?\.?|construid[ao]s?)|construidos?|edificad[ao]s?)\s*:?\s*([\d.,]+)\s*m`)
	reLand  = regexp.MustCompile(`(?i)(?:sup(?:erficie)?\.?\s*(?:terreno|total)|terreno)\s*:?\s*([\d.,]+)\s*m`)
)

func BuiltM2(text string) *float64 { return firstArea(text, reBuilt) }
func LandM2(text string) *float64  { return firstArea(text, reLand) }

func firstArea(text string, re *regexp.Regexp) *float64 {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	v, ok := ParseAmount(m[1])
	// 0 m² is the family's "not loaded" sentinel, same as the annual price.
	if !ok || v <= 0 {
		return nil
	}
	return model.Float(v)
}

// --- money ----------------------------------------------------------------

// ParseAmount reads Uruguayan number formatting: "7.500" is seven thousand five
// hundred, "2,500.00" is two thousand five hundred. Getting this backwards turns a
// USD 7.500 annual rent into USD 7.50.
func ParseAmount(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")

	// The separator is decided by how many digits follow it: exactly three means
	// thousands ("7.500" = 7500), one or two means decimals ("2.500,50").
	tailLen := func(i int) int { return len(s) - i - 1 }

	switch {
	case lastDot >= 0 && lastComma >= 0:
		// Both present: the rightmost one is the decimal separator.
		if lastComma > lastDot {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		if tailLen(lastComma) == 3 {
			s = strings.ReplaceAll(s, ",", "") // thousands
		} else {
			s = strings.Replace(s, ",", ".", 1) // decimals
		}
	case lastDot >= 0:
		if tailLen(lastDot) == 3 {
			s = strings.ReplaceAll(s, ".", "") // thousands: 7.500 / 3.100.000
		} else {
			s = strings.ReplaceAll(s[:lastDot], ".", "") + "." + s[lastDot+1:]
		}
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Currency maps the symbols these sites use. "$" alone means pesos in Uruguay.
func Currency(sym string) string {
	switch {
	case strings.Contains(sym, "U$S"), strings.Contains(strings.ToUpper(sym), "USD"),
		strings.Contains(sym, "US$"), strings.Contains(sym, "$US"):
		return "USD"
	case strings.Contains(sym, "$"):
		return "UYU"
	default:
		return ""
	}
}

// --- operation ------------------------------------------------------------

var (
	reAnual    = regexp.MustCompile(`(?i)\banual\b`)
	reInvernal = regexp.MustCompile(`(?i)\binvernal\b`)
	reSeason   = regexp.MustCompile(`(?i)(temporada|quincena|enero|febrero|marzo|diciembre|carnaval|semana santa|reveion|reveill?on)`)
)

// PeriodOperation classifies one row of the price table by its period label.
func PeriodOperation(period string) string {
	switch {
	case reAnual.MatchString(period):
		return model.OperationRentAnnual
	case reInvernal.MatchString(period):
		return model.OperationRentWinter
	case reSeason.MatchString(period):
		return model.OperationRentSeason
	default:
		return model.OperationRentUnknown
	}
}

// AnnualPrice picks the annual rent out of a price table.
//
// The trap this exists for: these sites print "Anual: U$S 0" to mean "we do not
// rent this one annually". Read literally it is the cheapest house in Maldonado and
// it would top every ranking. Zero means unknown, so it returns nothing.
func AnnualPrice(prices []model.PeriodPrice) (model.PeriodPrice, bool) {
	for _, p := range prices {
		if PeriodOperation(p.Period) == model.OperationRentAnnual && p.Amount > 0 {
			return p, true
		}
	}
	return model.PeriodPrice{}, false
}

// PropertyType maps the family's URL segment / label to a canonical type.
func PropertyType(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "casa"):
		return model.TypeHouse
	case strings.Contains(l, "apartamento"), strings.Contains(l, "apto"):
		return model.TypeApartment
	case strings.Contains(l, "chacra"):
		return model.TypeChacra
	case strings.Contains(l, "campo"):
		return model.TypeField
	case strings.Contains(l, "terreno"):
		return model.TypeLand
	case strings.Contains(l, "local"), strings.Contains(l, "oficina"):
		return model.TypeCommercial
	default:
		return model.TypeOther
	}
}
