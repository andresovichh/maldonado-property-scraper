// Command crawl scrapes the TERA family and writes normalised listings.
//
//	go run ./cmd/crawl                      # every TERA agency found by the scanner
//	go run ./cmd/crawl -limit 5             # a handful, for a quick look
//	go run ./cmd/crawl -types casas         # only houses
//
// Input is out/scan.json (produced by ./cmd/scanner); output is out/listings.json.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/discovery"
	"github.com/andresovichh/maldonado-property-scraper/internal/model"
	"github.com/andresovichh/maldonado-property-scraper/internal/normalize"
	"github.com/andresovichh/maldonado-property-scraper/internal/scraper"
)

func main() {
	var (
		scanFile      = flag.String("scan", "out/scan.json", "scanner output to take the agency list from")
		outFile       = flag.String("out", "out/listings.json", "where to write normalised listings")
		types         = flag.String("types", "casas", "comma-separated property types to crawl")
		operation     = flag.String("operation", "alquiler", "alquiler | venta")
		workers       = flag.Int("workers", 8, "agencies crawled in parallel")
		delay         = flag.Duration("delay", 1200*time.Millisecond, "minimum delay between requests to the same host")
		timeout       = flag.Duration("timeout", 25*time.Second, "per-request timeout")
		deadline      = flag.Duration("deadline", 30*time.Minute, "give up on the whole run after this")
		limit         = flag.Int("limit", 0, "crawl only the first N agencies (0 = all)")
		maxPerSite    = flag.Int("max-per-site", 500, "cap on listings kept per agency")
		maxPages      = flag.Int("max-pages", 25, "cap on index pages followed per property type")
		maxCandidates = flag.Int("max-candidates", 400, "cap on candidate URLs probed per non-TERA agency")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *deadline)
	defer cancel()

	agencies, err := teraAgencies(*scanFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *limit > 0 && *limit < len(agencies) {
		agencies = agencies[:*limit]
	}

	f := discovery.NewFetcher(*timeout, *delay)
	t := scraper.NewTera(f)
	gen := scraper.NewGeneric(f)

	fmt.Printf("crawling %d agencias con inventario (%s / %s)\n\n", len(agencies), *types, *operation)

	var (
		mu        sync.Mutex
		listings  []*model.Listing
		truncated []string
		stats     = map[string]int{}
	)

	var wg sync.WaitGroup
	sem := make(chan struct{}, *workers)
	for _, a := range agencies {
		wg.Add(1)
		go func(a agency) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			var (
				got []*model.Listing
				err error
			)
			if a.Engine == "tera" {
				got, err = crawlAgency(ctx, t, a, splitCSV(*types), *operation, *maxPerSite, *maxPages)
			} else {
				got, err = gen.Scrape(ctx, a.Domain, a.IndexURLs, *maxCandidates, *maxPerSite)
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				stats["agencias con error"]++
			}
			listings = append(listings, got...)
			if len(got) > 0 {
				stats["agencias con listings"]++
			}
			// A cap that silently trims inventory reads exactly like an agency that
			// simply has less — say it out loud instead.
			note := ""
			if len(got) >= *maxPerSite {
				note = "  ⚠ TRUNCADA por -max-per-site"
				truncated = append(truncated, a.Domain)
			}
			fmt.Printf("  %-38s %3d listings%s\n", truncate(a.Domain, 38), len(got), note)
		}(a)
	}
	wg.Wait()

	for _, l := range listings {
		if l.Operation != nil {
			stats[*l.Operation]++
		} else {
			stats["sin operación"]++
		}
	}

	if err := writeJSON(*outFile, listings); err != nil {
		fmt.Fprintln(os.Stderr, "error writing listings:", err)
		os.Exit(1)
	}

	fmt.Printf("\n%d listings\n", len(listings))
	for _, k := range []string{
		model.OperationRentAnnual, model.OperationRentSeason, model.OperationRentWinter,
		"sin operación", "agencias con listings", "agencias con error",
	} {
		if stats[k] > 0 {
			fmt.Printf("  %-22s %4d\n", k, stats[k])
		}
	}
	if len(truncated) > 0 {
		fmt.Printf("\n⚠ %d agencias truncadas en %d listings — subí -max-per-site para completarlas:\n   %s\n",
			len(truncated), *maxPerSite, strings.Join(truncated, ", "))
	}
	fmt.Printf("\nescrito en %s\n", *outFile)
}

type agency struct {
	Name      string
	Domain    string
	Base      string
	Engine    string
	IndexURLs []string // where the scanner already found inventory (non-TERA sites)
}

func teraAgencies(path string) ([]agency, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w (¿corriste ./cmd/scanner primero?)", err)
	}
	var results []discovery.Result
	if err := json.Unmarshal(b, &results); err != nil {
		return nil, err
	}
	var out []agency
	for _, r := range results {
		// Every agency the scanner found real inventory on, whatever engine it runs.
		// Restricting this to TERA was leaving 19 agencies on the table.
		if !r.HasInventory || r.RobotsDisallowed || r.RequiresJavaScript {
			continue
		}
		base := r.FinalURL
		if base == "" {
			base = "https://" + r.Agency.Domain
		}
		out = append(out, agency{
			Name:      r.Agency.Name,
			Domain:    r.Agency.Domain,
			Base:      baseOf(base),
			Engine:    string(r.Engine),
			IndexURLs: r.PropertyPages,
		})
	}
	return out, nil
}

func crawlAgency(ctx context.Context, t *scraper.Tera, a agency, types []string, operation string, maxPerSite, maxPages int) ([]*model.Listing, error) {
	var urls []string
	seen := map[string]bool{}

	for _, pt := range types {
		// periodo=17 asks the site for annual rentals. Some sites honour it, some
		// ignore it; either way the price table decides, so this is only a saving.
		idx := scraper.ListingIndexURL(a.Base, pt, operation, scraper.PeriodoAnual)
		found, err := t.DetailURLs(ctx, idx, maxPages)
		if err != nil {
			continue
		}
		for _, u := range found {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("%s: sin listings", a.Domain)
	}
	if len(urls) > maxPerSite {
		urls = urls[:maxPerSite]
	}

	out := make([]*model.Listing, 0, len(urls))
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		raw, err := t.ScrapeDetail(ctx, a.Domain, u)
		if err != nil {
			continue
		}
		out = append(out, normalize.FromRaw(raw))
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	return enc.Encode(v)
}

// baseOf keeps just scheme://host — the family's paths are appended to it.
func baseOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		if j := strings.Index(u[i+3:], "/"); j >= 0 {
			return u[:i+3+j]
		}
	}
	return u
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
