package scraper

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/andresovichh/maldonado-property-scraper/internal/discovery"
	"github.com/andresovichh/maldonado-property-scraper/internal/model"
	"github.com/andresovichh/maldonado-property-scraper/internal/normalize"
)

// Generic scrapes the agencies that are not part of the TERA family — the WordPress,
// Wix and hand-rolled sites, 19 more agencies with server-side inventory.
//
// It works because the expensive half of the TERA adapter was never TERA-specific:
// ParseDetail reads Open Graph tags and labelled text, which every one of these
// sites emits. What does not carry over is how you find a detail page, since there
// is no shared URL convention. So instead of knowing the convention, this adapter
// guesses candidates from the index and then VERIFIES each one by checking that the
// page really parses as a property. A wrong guess costs one request and is dropped.
type Generic struct {
	f *discovery.Fetcher
}

func NewGeneric(f *discovery.Fetcher) *Generic { return &Generic{f: f} }

func (g *Generic) Name() string { return "generic" }

var (
	reHref = regexp.MustCompile(`href=["']([^"'#]+)["']`)

	// Path words that show up in listing detail URLs across these sites.
	reListingWord = regexp.MustCompile(`(?i)(propiedad|inmueble|ficha|detalle|property|listing|alquiler|venta|casa|apartamento|apto|chacra|campo|terreno)`)

	// A numeric or slug-with-id segment is the strongest hint that a URL is one
	// specific property rather than a category page.
	reIDSegment = regexp.MustCompile(`/(\d{1,7})(?:/|$)|[-_](\d{2,7})(?:/|$)`)

	// Pages that are never inventory, however listing-ish their path looks.
	reNotListing = regexp.MustCompile(`(?i)(contacto|nosotros|quienes|about|blog|noticias?|novedades|tasacion|servicios|privacidad|terminos|cookies|login|admin|wp-(admin|login|json)|\.(jpg|jpeg|png|webp|gif|pdf|css|js|zip|mp4)$)`)
)

// CandidateURLs picks plausible property-detail links out of an index page.
//
// Deliberately permissive: a false positive costs one fetch that gets discarded by
// LooksLikeListing, while a false negative loses a property for good.
func CandidateURLs(body, base string, max int) []string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var strong, weak []string

	for _, m := range reHref.FindAllStringSubmatch(body, -1) {
		ref := htmlUnescape(strings.TrimSpace(m[1]))
		if ref == "" || strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "tel:") ||
			strings.HasPrefix(ref, "javascript:") {
			continue
		}
		u, err := baseURL.Parse(ref)
		if err != nil || u.Host != baseURL.Host {
			continue // same site only
		}
		u.Fragment = ""
		s := u.String()
		if seen[s] || s == base || reNotListing.MatchString(u.Path) {
			continue
		}
		seen[s] = true

		hasID := reIDSegment.MatchString(u.Path)
		hasWord := reListingWord.MatchString(u.Path)
		switch {
		case hasID && hasWord:
			strong = append(strong, s)
		case hasID || hasWord:
			weak = append(weak, s)
		}
	}

	// Strong candidates first: with a per-site budget we want the likely ones.
	out := append(strong, weak...)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// LooksLikeListing reports whether a fetched page really is one property.
//
// This is the check that lets the adapter guess URLs without knowing any site's
// conventions: a category page or an "about us" has no price-and-rooms pair, a
// property page does.
func LooksLikeListing(l *model.Listing) bool {
	hasRooms := l.Bedrooms != nil || l.Bathrooms != nil
	hasPrice := (l.Price != nil && *l.Price > 0) || len(l.PeriodPrices) > 0
	return hasRooms && hasPrice
}

// Scrape walks one agency's index pages and returns the listings it could verify.
func (g *Generic) Scrape(ctx context.Context, agencyDomain string, indexURLs []string, maxCandidates, maxKeep int) ([]*model.Listing, error) {
	seen := map[string]bool{}
	var candidates []string

	for _, idx := range indexURLs {
		res, err := g.f.Get(ctx, idx)
		if err != nil || res.Status != 200 {
			continue
		}
		for _, u := range CandidateURLs(res.Body, res.URL, maxCandidates) {
			if !seen[u] {
				seen[u] = true
				candidates = append(candidates, u)
			}
		}
		// Some of these are TERA-shaped even when the scanner said otherwise; if the
		// family's detail pattern is present, trust it over the guesses.
		if exact := ExtractDetailURLs(res.Body, res.URL); len(exact) > 0 {
			candidates = append(exact, candidates...)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%s: sin candidatos", agencyDomain)
	}
	if maxCandidates > 0 && len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	var out []*model.Listing
	for _, u := range candidates {
		if ctx.Err() != nil || (maxKeep > 0 && len(out) >= maxKeep) {
			break
		}
		res, err := g.f.Get(ctx, u)
		if err != nil || res.Status != 200 {
			continue
		}
		l := normalize.FromRaw(ParseDetail(agencyDomain, res.URL, res.Body))
		if LooksLikeListing(l) {
			out = append(out, l)
		}
	}
	return out, nil
}
