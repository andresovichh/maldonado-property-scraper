// Package scraper turns agency websites into raw listings.
package scraper

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/discovery"
	"github.com/andresovichh/maldonado-property-scraper/internal/model"
	"github.com/andresovichh/maldonado-property-scraper/internal/normalize"
)

// Tera scrapes the TERA CRM family — 67 of the 86 Maldonado agencies with usable
// inventory, per the scanner.
//
// What the family actually shares (measured, not assumed):
//
//	✓ listing URLs      /{tipo}/en-{operación}/     e.g. /casas/en-alquiler/
//	✓ detail URLs       /{Tipo}/{id}                e.g. /Casa/1271
//	✓ image CDN         ri.com.uy
//	✓ search form       ?periodo=&dormitorios=&banos=&ciudad=
//	✓ field labels      "Dormitorios", "Baños", "Precios de Alquiler"
//	✗ HTML templates    37 different card layouts across 40 sites sampled
//
// So this adapter navigates by URL and extracts by LABEL, never by CSS selector.
// A card parser would have to be rewritten per agency; a label parser does not.
type Tera struct {
	f *discovery.Fetcher
}

func NewTera(f *discovery.Fetcher) *Tera { return &Tera{f: f} }

func (t *Tera) Name() string { return "tera" }

// PropertyTypes are the URL segments the family uses for the listing index.
var PropertyTypes = []string{"casas", "apartamentos", "chacras", "campos", "terrenos", "locales"}

// reDetailURL matches the family's detail links. The type segment is capitalised in
// the URL ("/Casa/1271") which is what keeps this from matching ordinary pages.
var reDetailURL = regexp.MustCompile(`(?:href=["'])((?:https?://[^"']+)?/(?:Casa|Apartamento|Chacra|Campo|Terreno|Local|Oficina|Galpon|Galp[oó]n)/(\d+))["']`)

// ListingIndexURL builds the index for one property type and operation.
//
// periodo is the family's rental-period filter; 17 is "Anual". It is passed through
// even though some sites ignore it — where it works it saves us fetching seasonal
// listings we would only throw away.
func ListingIndexURL(base, propertyType, operation string, periodo int) string {
	u := strings.TrimSuffix(base, "/") + "/" + propertyType + "/en-" + operation + "/"
	if periodo > 0 {
		u += fmt.Sprintf("?periodo=%d", periodo)
	}
	return u
}

// PeriodoAnual is the family's select value for annual rentals.
const PeriodoAnual = 17

// rePageLink matches the family's pager: <a href='...?pagina=2' class='page-link'>.
//
// Note the single quotes — the pager markup uses them while the rest of the page
// uses double, which is why an earlier double-quote-only probe concluded there was
// no pagination at all. bintang.com.uy advertises "68 Resultados" and shows 18.
var rePageLink = regexp.MustCompile(`href=['"]([^'"]*[?&]pagina=(\d+)[^'"]*)['"]`)

// reResultCount reads the "68 Resultados Encontrados" line, which lets us tell a
// complete index from a truncated one.
var reResultCount = regexp.MustCompile(`(?i)(\d+)\s*Resultados?\s*Encontrad`)

// DetailURLs returns every listing detail URL for an index, following the pager.
//
// maxPages bounds the walk; 0 means "as many as the pager advertises".
func (t *Tera) DetailURLs(ctx context.Context, indexURL string, maxPages int) ([]string, error) {
	res, err := t.f.Get(ctx, indexURL)
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("index %s: status %d", indexURL, res.Status)
	}

	seen := map[string]bool{}
	var out []string
	addAll := func(body, base string) int {
		added := 0
		for _, u := range ExtractDetailURLs(body, base) {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
				added++
			}
		}
		return added
	}
	addAll(res.Body, res.URL)

	pages := PageURLs(res.Body, res.URL)
	if maxPages > 0 && len(pages) > maxPages {
		pages = pages[:maxPages]
	}
	for _, p := range pages {
		if ctx.Err() != nil {
			break
		}
		pr, err := t.f.Get(ctx, p)
		if err != nil || pr.Status != 200 {
			continue
		}
		// A pager that keeps serving the same rows means we have reached the end;
		// stop rather than walk a loop of identical pages.
		if addAll(pr.Body, pr.URL) == 0 {
			break
		}
	}
	return out, nil
}

// PageURLs returns the pager's pages 2..N, in order and deduplicated. Page 1 is the
// index we already fetched.
func PageURLs(body, base string) []string {
	seen := map[int]string{}
	for _, m := range rePageLink.FindAllStringSubmatch(body, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 2 {
			continue
		}
		if _, ok := seen[n]; !ok {
			seen[n] = absolute(base, htmlUnescape(m[1]))
		}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, seen[n])
	}
	return out
}

// ResultCount reports how many results the index says it has, when it says so.
func ResultCount(body string) (int, bool) {
	m := reResultCount.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// ExtractDetailURLs pulls /{Tipo}/{id} links out of an index page, deduplicated and
// made absolute.
func ExtractDetailURLs(body, base string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reDetailURL.FindAllStringSubmatch(body, -1) {
		u := absolute(base, m[1])
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// ScrapeDetail fetches one property page and captures it as a RawListing.
//
// Nothing is interpreted here beyond what it takes to find the fields; the text is
// kept as printed so the normaliser can be rerun over history later.
func (t *Tera) ScrapeDetail(ctx context.Context, agencyDomain, detailURL string) (*model.RawListing, error) {
	res, err := t.f.Get(ctx, detailURL)
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("detail %s: status %d", detailURL, res.Status)
	}
	return ParseDetail(agencyDomain, res.URL, res.Body), nil
}

var (
	reOG      = regexp.MustCompile(`<meta[^>]*property=["']og:([a-z_:]+)["'][^>]*content=["']([^"']*)["']`)
	reOGrev   = regexp.MustCompile(`<meta[^>]*content=["']([^"']*)["'][^>]*property=["']og:([a-z_:]+)["']`)
	reIDInURL = regexp.MustCompile(`/([A-Za-zÁ-Úá-ú]+)/(\d+)/?$`)
)

// ParseDetail extracts the raw fields from a property page.
func ParseDetail(agencyDomain, pageURL, body string) *model.RawListing {
	raw := map[string]any{}

	// Open Graph is the one structured surface every site in the family emits.
	og := map[string]string{}
	for _, m := range reOG.FindAllStringSubmatch(body, -1) {
		og[m[1]] = htmlUnescape(m[2])
	}
	for _, m := range reOGrev.FindAllStringSubmatch(body, -1) {
		if _, ok := og[m[2]]; !ok {
			og[m[2]] = htmlUnescape(m[1])
		}
	}
	if len(og) > 0 {
		raw["og"] = og
	}

	text := VisibleText(body)
	raw["text"] = text

	if m := reIDInURL.FindStringSubmatch(strings.TrimSuffix(pageURL, "/")); m != nil {
		raw["url_type"] = m[1]
		raw["source_id"] = m[2]
	}

	if prices := ParsePriceTable(text); len(prices) > 0 {
		raw["period_prices"] = prices
	}

	sourceID, _ := raw["source_id"].(string)
	return &model.RawListing{
		AgencyDomain: agencyDomain,
		SourceID:     sourceID,
		URL:          pageURL,
		Raw:          raw,
		ScrapedAt:    time.Now().UTC(),
	}
}

var (
	reStrip     = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<!--.*?-->`)
	reTag       = regexp.MustCompile(`<[^>]+>`)
	reWhitespce = regexp.MustCompile(`\s+`)
)

// VisibleText flattens a page to the words a reader would see. Everything the
// normaliser does works off this, which is what makes the adapter survive 37
// different HTML templates.
func VisibleText(body string) string {
	s := reStrip.ReplaceAllString(body, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = htmlUnescape(s)
	return strings.TrimSpace(reWhitespce.ReplaceAllString(s, " "))
}

// rePriceRow matches one row of "Precios de Alquiler": a period label followed by a
// currency and an amount.
var rePriceRow = regexp.MustCompile(
	`(?i)((?:1[ªa]?|2[ªa]?|1er|2da)?\s*(?:Quin\.?|Quincena)?\s*` +
		`(?:Enero|Febrero|Marzo|Diciembre|Anual|Invernal|Carnaval|Semana\s+Santa|Reveion|Reveill?on)` +
		`(?:\s*(?:en|\()?\s*(?:D[oó]lares|Pesos)\)?)?)\s*:?\s*(U\$S|USD|US\$|\$US|\$)\s*([\d][\d.,]*)`)

// ParsePriceTable reads the per-period rental prices.
//
// These are the numbers that decide whether a listing is even relevant: a January
// price of USD 10.000 is a fortnight of summer, not a monthly rent. Keeping every
// row lets the ranker tell annual from temporada instead of guessing.
func ParsePriceTable(text string) []model.PeriodPrice {
	idx := strings.Index(strings.ToLower(text), "precios de alquiler")
	scope := text
	if idx >= 0 {
		scope = text[idx:]
		// The table is short; stop before the page's unrelated tail.
		if len(scope) > 1200 {
			scope = scope[:1200]
		}
	}

	var out []model.PeriodPrice
	seen := map[string]bool{}
	for _, m := range rePriceRow.FindAllStringSubmatch(scope, -1) {
		period := strings.TrimSpace(reWhitespce.ReplaceAllString(m[1], " "))
		amount, ok := normalize.ParseAmount(m[3])
		if !ok {
			continue
		}
		key := strings.ToLower(period)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.PeriodPrice{
			Period:   period,
			Currency: normalize.Currency(m[2]),
			Amount:   amount,
		})
	}
	return out
}

func absolute(base, ref string) string {
	if strings.HasPrefix(ref, "http") {
		return ref
	}
	base = strings.TrimSuffix(base, "/")
	if i := strings.Index(base[8:], "/"); i >= 0 {
		base = base[:8+i]
	}
	return base + ref
}

var htmlEntities = strings.NewReplacer(
	"&nbsp;", " ", "&amp;", "&", "&quot;", `"`, "&#39;", "'", "&apos;", "'",
	"&lt;", "<", "&gt;", ">", "&aacute;", "á", "&eacute;", "é", "&iacute;", "í",
	"&oacute;", "ó", "&uacute;", "ú", "&ntilde;", "ñ", "&Aacute;", "Á",
	"&Eacute;", "É", "&Iacute;", "Í", "&Oacute;", "Ó", "&Uacute;", "Ú", "&Ntilde;", "Ñ",
)

func htmlUnescape(s string) string { return htmlEntities.Replace(s) }
