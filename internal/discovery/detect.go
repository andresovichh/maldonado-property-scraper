package discovery

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// UserAgent identifies us honestly. We are indexing public listings; we do not
// pretend to be a browser and we do not try to get around anything.
const UserAgent = "MaldonadoPropertyScanner/0.1 (+https://github.com/andresovichh/maldonado-property-scraper)"

// Engine is the technology family a site belongs to. The whole point of the scanner
// is that a handful of these cover most of the ~150 agencies, so we write a handful
// of scrapers instead of 150.
type Engine string

const (
	EngineTera        Engine = "tera"        // TERA CRM (tera.com.uy) — the real-estate backend
	EngineWordPress   Engine = "wordpress"   //
	EngineWix         Engine = "wix"         //
	EngineSquarespace Engine = "squarespace" //
	EngineWebflow     Engine = "webflow"     //
	EngineCustom      Engine = "custom"      // server-rendered HTML we don't recognise
	EngineUnknown     Engine = "unknown"     // site did not answer
)

// Result is what the scanner learns about one agency.
type Result struct {
	Agency Agency `json:"agency"`

	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	FinalURL   string `json:"final_url,omitempty"`
	Error      string `json:"error,omitempty"`

	Engine Engine `json:"engine"`
	// Developer is the web studio behind the site (e.g. Sierra), which is NOT the
	// same thing as the CRM that holds the inventory. Sierra builds sites that run
	// on TERA; keeping them apart avoids a whole class of wrong conclusions.
	Developer string   `json:"developer,omitempty"`
	Signals   []string `json:"signals,omitempty"`

	// TeraToken is the per-agency id TERA embeds in its chatbot snippet
	// (var inmToken='...'). Present only on part of the family, but when it is
	// there it identifies the agency inside TERA.
	TeraToken string `json:"tera_token,omitempty"`

	PropertyPages []string `json:"property_pages,omitempty"`
	APICandidates []string `json:"api_candidates,omitempty"`
	Sitemaps      []string `json:"sitemaps,omitempty"`

	RequiresJavaScript bool `json:"requires_javascript"`
	// HasInventory means we actually saw listing-shaped content (prices, bedroom
	// counts) in server-rendered HTML.
	HasInventory bool `json:"has_inventory"`
	ListingHits  int  `json:"listing_hits,omitempty"`

	RobotsDisallowed bool          `json:"robots_disallowed,omitempty"`
	Elapsed          time.Duration `json:"-"`

	// Classification bookkeeping. ri.com.uy is a shared image CDN used by more
	// than just TERA, so on its own it only earns a "maybe" that the URL-scheme
	// probe has to confirm. Unexported so they stay out of the JSON.
	teraStrong  bool
	teraWeak    bool
	teraPathHit bool
}

// teraPaths is the URL scheme shared by every TERA/Sierra site checked so far:
// /{property-type}/en-{operation}/. Probing one of these both finds the inventory
// page and confirms family membership.
var teraPaths = []string{"/casas/en-alquiler/", "/casas/en-venta/", "/apartamentos/en-alquiler/"}

// genericPaths are the usual places a listing index lives when the site is not TERA.
var genericPaths = []string{
	"/propiedades", "/propiedades/", "/inmuebles", "/inmuebles/", "/alquileres",
	"/alquiler", "/venta", "/ventas", "/buscar", "/busqueda", "/listado",
	"/properties", "/search",
}

var (
	reInmToken   = regexp.MustCompile(`inmToken\s*=\s*['"]([^'"]+)['"]`)
	rePrice      = regexp.MustCompile(`(?i)(USD|U\$S|\$US|US\$)\s?[\d][\d.,]{2,}`)
	reRooms      = regexp.MustCompile(`(?i)\b\d+\s*(dormitorio|dorm\b|ambiente|baño|bano)`)
	reScriptTags = regexp.MustCompile(`(?i)<script\b`)
	reJSONURL    = regexp.MustCompile(`["'](/[^"'\s]*(?:api|json|ajax)[^"'\s]*)["']`)
)

// Detect probes one agency and reports how its site is built.
//
// It is deliberately cheap: a homepage fetch, robots.txt, one sitemap HEAD and at
// most a couple of candidate listing pages. We are measuring ~150 sites, not
// crawling them.
func Detect(ctx context.Context, f *Fetcher, a Agency) Result {
	start := time.Now()
	r := Result{Agency: a, Engine: EngineUnknown}
	defer func() { r.Elapsed = time.Since(start) }()

	base := a.Website
	if base == "" && a.Domain != "" {
		base = "https://" + a.Domain
	}
	if base == "" {
		r.Error = "agency has no website"
		return r
	}

	home, err := f.Get(ctx, base)
	if err != nil {
		// Plenty of these sites only answer on the apex or only on www; one retry
		// on the other form turns a lot of false "site roto" into real data.
		if alt := flipWWW(base); alt != "" {
			if home2, err2 := f.Get(ctx, alt); err2 == nil {
				home, err = home2, nil
			}
		}
	}
	if err != nil {
		r.Error = trimErr(err)
		return r
	}

	r.Reachable = true
	r.StatusCode = home.Status
	r.FinalURL = home.URL
	body := home.Body

	r.Robots(ctx, f, home.URL)
	r.classify(body, home.Headers)
	r.findSitemaps(ctx, f, home.URL)
	r.findAPICandidates(body, home.URL)
	r.probeListings(ctx, f, home.URL, body)

	return r
}

// classify works out the engine and developer from homepage markup.
func (r *Result) classify(body, headers string) {
	low := strings.ToLower(body + "\n" + headers)
	add := func(s string) { r.Signals = append(r.Signals, s) }

	// --- TERA CRM ---------------------------------------------------------
	// Three independent markers; any one of them is enough, and they were all
	// observed together on nicolasdemodena / javiersena / sader / cristinanaum.
	if m := reInmToken.FindStringSubmatch(body); m != nil {
		r.TeraToken = m[1]
		add("inmToken")
		r.teraStrong = true
	}
	for _, marker := range []struct{ needle, signal string }{
		{"tera.com.uy", "tera.com.uy-link"},
		{"tera.uy/bot", "tera-bot"},
		{"terajsbot", "tera-bot"},
	} {
		if strings.Contains(low, marker.needle) {
			add(marker.signal)
			r.teraStrong = true
		}
	}
	// Weak: the family serves its photos from ri.com.uy, but so do other Uruguayan
	// real-estate systems. Confirmed later only if the /{tipo}/en-{operacion}/ URL
	// scheme also answers.
	if strings.Contains(low, "ri.com.uy") {
		add("ri.com.uy-images")
		r.teraWeak = true
	}

	// Sierra builds the sites; it is the developer, not the inventory backend.
	if strings.Contains(low, "sierra.com.uy") || strings.Contains(low, "sierra soluciones") {
		r.Developer = "sierra"
		add("sierra-footer")
	}

	switch {
	case r.teraStrong:
		r.Engine = EngineTera
	case strings.Contains(low, "wp-content") || strings.Contains(low, "wp-json") || strings.Contains(low, "wordpress"):
		r.Engine = EngineWordPress
		add("wp-content")
	case strings.Contains(low, "wix.com") || strings.Contains(low, "wixstatic"):
		r.Engine = EngineWix
	case strings.Contains(low, "squarespace"):
		r.Engine = EngineSquarespace
	case strings.Contains(low, "webflow"):
		r.Engine = EngineWebflow
	default:
		r.Engine = EngineCustom
	}

	sort.Strings(r.Signals)
	r.Signals = dedupe(r.Signals)
}

// Robots records whether robots.txt forbids the paths we are about to probe. We do
// not need the full spec here — a Disallow: / under a group that applies to us is
// the only case that should stop the scan.
func (r *Result) Robots(ctx context.Context, f *Fetcher, base string) {
	u, err := url.Parse(base)
	if err != nil {
		return
	}
	res, err := f.Get(ctx, u.Scheme+"://"+u.Host+"/robots.txt")
	if err != nil || res.Status != http.StatusOK {
		return
	}
	applies := false
	for _, line := range strings.Split(res.Body, "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if k, v, ok := strings.Cut(line, ":"); ok {
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "user-agent":
				applies = v == "*"
			case "disallow":
				if applies && v == "/" {
					r.RobotsDisallowed = true
					return
				}
			}
		}
	}
}

func (r *Result) findSitemaps(ctx context.Context, f *Fetcher, base string) {
	u, err := url.Parse(base)
	if err != nil {
		return
	}
	for _, p := range []string{"/sitemap.xml", "/sitemap_index.xml"} {
		res, err := f.Get(ctx, u.Scheme+"://"+u.Host+p)
		if err == nil && res.Status == http.StatusOK && strings.Contains(res.Body, "<urlset") || (err == nil && strings.Contains(res.Body, "<sitemapindex")) {
			r.Sitemaps = append(r.Sitemaps, u.Scheme+"://"+u.Host+p)
		}
	}
}

// findAPICandidates pulls JSON-ish endpoints out of the page. Hitting an endpoint
// the frontend already uses beats re-rendering HTML, so these are worth recording
// even when we can't confirm them yet.
func (r *Result) findAPICandidates(body, base string) {
	seen := map[string]bool{}
	for _, m := range reJSONURL.FindAllStringSubmatch(body, -1) {
		p := m[1]
		if len(p) > 120 || strings.Contains(p, "{") {
			continue
		}
		// Static assets shaped like /api/... are noise.
		if strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".png") {
			continue
		}
		if !seen[p] {
			seen[p] = true
			r.APICandidates = append(r.APICandidates, absURL(base, p))
		}
	}
	if r.Engine == EngineWordPress {
		r.APICandidates = append(r.APICandidates, absURL(base, "/wp-json/wp/v2/"))
	}
	sort.Strings(r.APICandidates)
	if len(r.APICandidates) > 8 {
		r.APICandidates = r.APICandidates[:8]
	}
}

// probeListings looks for a page that actually holds inventory, and decides whether
// that inventory is server-rendered or needs a browser.
func (r *Result) probeListings(ctx context.Context, f *Fetcher, base, homeBody string) {
	if r.RobotsDisallowed {
		return
	}

	// The homepage itself often carries featured listings — cheapest possible signal.
	if hits := listingHits(homeBody); hits >= 3 {
		r.HasInventory = true
		r.ListingHits = hits
		r.PropertyPages = append(r.PropertyPages, base)
	}

	paths := genericPaths
	if r.teraStrong || r.teraWeak {
		paths = append(append([]string{}, teraPaths...), genericPaths...)
	}

	tried := 0
	for _, p := range paths {
		if tried >= 4 || (r.HasInventory && len(r.PropertyPages) >= 2) {
			break
		}
		res, err := f.Get(ctx, absURL(base, p))
		tried++
		if err != nil || res.Status != http.StatusOK {
			continue
		}
		if isTeraPath(p) {
			r.teraPathHit = true
		}
		hits := listingHits(res.Body)
		if hits >= 3 {
			r.HasInventory = true
			if hits > r.ListingHits {
				r.ListingHits = hits
			}
			r.PropertyPages = append(r.PropertyPages, res.URL)
		} else if looksLikeSPA(res.Body) {
			r.RequiresJavaScript = true
			r.PropertyPages = append(r.PropertyPages, res.URL)
		}
	}

	// Promote a "maybe TERA" only if the family's URL scheme actually answered.
	if r.teraWeak && !r.teraStrong && r.teraPathHit && r.Engine != EngineTera {
		r.Engine = EngineTera
		r.Signals = append(r.Signals, "tera-url-scheme")
		sort.Strings(r.Signals)
	}

	r.PropertyPages = dedupe(r.PropertyPages)
	// A site that answers but never shows a price in raw HTML is either a SPA or
	// has no inventory online; the SPA check separates the two.
	if !r.HasInventory && looksLikeSPA(homeBody) {
		r.RequiresJavaScript = true
	}
}

// listingHits scores how much a page looks like a list of properties: prices plus
// room counts. Two independent signals keep a page of prose about prices from
// counting as inventory.
func isTeraPath(p string) bool {
	for _, t := range teraPaths {
		if t == p {
			return true
		}
	}
	return false
}

func listingHits(body string) int {
	prices := len(rePrice.FindAllString(body, 40))
	rooms := len(reRooms.FindAllString(body, 40))
	if prices == 0 || rooms == 0 {
		return 0
	}
	return min(prices, rooms)
}

// looksLikeSPA flags pages that are mostly script and almost no text — the shape of
// a client-rendered app, which is the only case where we need a real browser.
func looksLikeSPA(body string) bool {
	scripts := len(reScriptTags.FindAllString(body, -1))
	text := len(stripTags(body))
	return scripts >= 3 && text < 1500
}

var reTags = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)

func stripTags(s string) string {
	return strings.TrimSpace(reTags.ReplaceAllString(s, " "))
}

func absURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(u).String()
}

func flipWWW(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(u.Host, "www.") {
		u.Host = strings.TrimPrefix(u.Host, "www.")
	} else {
		u.Host = "www." + u.Host
	}
	return u.String()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func trimErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && len(s) > 90 {
		s = s[i+2:]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
