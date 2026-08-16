// Package discovery finds the real-estate agencies of Maldonado and works out how
// their websites are built, so the crawler can be written against a handful of
// technology families instead of one scraper per agency.
package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// CIPEMNominaURL is the members roster of the Cámara Inmobiliaria de Punta del Este
// y Maldonado. It is the seed registry for the whole project.
//
// Note: the domain is cipem.org.uy — NOT cipem.com.uy, which does not resolve.
const CIPEMNominaURL = "https://cipem.org.uy/socios/nomina/"

// Agency is one member of the chamber.
type Agency struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Website string `json:"website,omitempty"`
	Email   string `json:"email,omitempty"`
	Phone   string `json:"phone,omitempty"`
	Address string `json:"address,omitempty"`
}

// FetchAgencies scrapes the CIPEM roster.
//
// The roster is a WordPress page rendered server-side: one div.vcex-post-type-entry
// per member, the name in the entry title, and the contact details in a <ul> whose
// links are mailto: / tel: / the agency's own site. No JavaScript involved.
func FetchAgencies(ctx context.Context, f *Fetcher) ([]Agency, error) {
	res, err := f.Get(ctx, CIPEMNominaURL)
	if err != nil {
		return nil, fmt.Errorf("fetch roster: %w", err)
	}
	if res.Status != http.StatusOK {
		return nil, fmt.Errorf("roster returned %d", res.Status)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(res.Body))
	if err != nil {
		return nil, fmt.Errorf("parse roster: %w", err)
	}
	return parseNomina(doc), nil
}

func parseNomina(doc *goquery.Document) []Agency {
	seen := map[string]bool{}
	var out []Agency

	doc.Find(".vcex-post-type-entry").Each(func(_ int, s *goquery.Selection) {
		a := Agency{Name: strings.TrimSpace(s.Find(".entry-title").First().Text())}

		s.Find("li").Each(func(_ int, li *goquery.Selection) {
			link := li.Find("a").First()
			href, ok := link.Attr("href")
			if !ok {
				// A bare <li> with no link is the street address.
				if a.Address == "" {
					a.Address = strings.TrimSpace(li.Text())
				}
				return
			}
			switch {
			case strings.HasPrefix(href, "mailto:"):
				a.Email = strings.TrimPrefix(href, "mailto:")
			case strings.HasPrefix(href, "tel:"):
				if a.Phone == "" {
					a.Phone = strings.TrimPrefix(href, "tel:")
				}
			case strings.HasPrefix(href, "http"):
				if a.Website == "" && !isSocialOrChamber(href) {
					a.Website = href
					a.Domain = HostOf(href)
				}
			}
		})

		// A member with no website is useless to the crawler, but keep the ones that
		// at least have a name so the totals stay honest.
		if a.Name == "" {
			return
		}
		key := a.Domain
		if key == "" {
			key = "name:" + strings.ToLower(a.Name)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, a)
	})

	return out
}

// socialHosts are links that appear inside member cards but are never the agency's
// own inventory site.
var socialHosts = []string{
	"facebook.", "instagram.", "twitter.", "x.com", "linkedin.", "youtube.",
	"api.whatsapp.com", "wa.me", "cipem.org.uy", "cipem.com.uy", "goo.gl",
	"google.com", "maps.app.goo.gl", "t.me", "tiktok.",
}

func isSocialOrChamber(raw string) bool {
	h := HostOf(raw)
	for _, s := range socialHosts {
		if strings.Contains(h, s) {
			return true
		}
	}
	return false
}

// HostOf returns the bare hostname of a URL, without "www." and lowercased.
//
// The trailing dot is the FQDN root: the roster really does list
// "http://www.tambolini.com./", which is valid but would otherwise travel through
// the whole pipeline as a distinct domain from "tambolini.com".
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return strings.TrimPrefix(h, "www.")
}
