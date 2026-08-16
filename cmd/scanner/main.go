// Command scanner builds the technology inventory of Maldonado's real-estate
// agencies: who they are, how their sites are built, and where the listings live.
//
// This runs BEFORE any scraper is written. The whole bet of the project is that a
// handful of technology families cover most of the ~150 agencies, so we measure
// first and only then decide how many scrapers we actually need.
//
//	go run ./cmd/scanner                 # full run against the CIPEM roster
//	go run ./cmd/scanner -limit 10       # quick smoke test
//	go run ./cmd/scanner -domains a.com,b.com
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/discovery"
)

func main() {
	var (
		outDir   = flag.String("out", "out", "directory for scan results")
		workers  = flag.Int("workers", 12, "agencies scanned in parallel (each host is still serialised)")
		delay    = flag.Duration("delay", 800*time.Millisecond, "minimum delay between two requests to the same host")
		timeout  = flag.Duration("timeout", 25*time.Second, "per-request timeout")
		deadline = flag.Duration("deadline", 25*time.Minute, "give up on the whole run after this")
		limit    = flag.Int("limit", 0, "scan only the first N agencies (0 = all)")
		domains  = flag.String("domains", "", "comma-separated domains to scan instead of the CIPEM roster")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *deadline)
	defer cancel()

	fetcher := discovery.NewFetcher(*timeout, *delay)

	agencies, err := loadAgencies(ctx, fetcher, *domains)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *limit > 0 && *limit < len(agencies) {
		agencies = agencies[:*limit]
	}
	fmt.Printf("scanning %d agencies (%d workers, %v per-host delay)\n\n", len(agencies), *workers, *delay)

	results := scan(ctx, fetcher, agencies, *workers)

	sort.Slice(results, func(i, j int) bool {
		if results[i].Engine != results[j].Engine {
			return results[i].Engine < results[j].Engine
		}
		return results[i].Agency.Name < results[j].Agency.Name
	})

	if err := writeResults(*outDir, results); err != nil {
		fmt.Fprintln(os.Stderr, "error writing results:", err)
		os.Exit(1)
	}
	printSummary(results)
	fmt.Printf("\nwrote %s/scan.json and %s/scan.csv\n", *outDir, *outDir)
}

func loadAgencies(ctx context.Context, f *discovery.Fetcher, domains string) ([]discovery.Agency, error) {
	if domains != "" {
		var out []discovery.Agency
		for _, d := range strings.Split(domains, ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			out = append(out, discovery.Agency{Name: d, Domain: d, Website: "https://" + d})
		}
		return out, nil
	}
	fmt.Println("fetching CIPEM roster…")
	return discovery.FetchAgencies(ctx, f)
}

func scan(ctx context.Context, f *discovery.Fetcher, agencies []discovery.Agency, workers int) []discovery.Result {
	jobs := make(chan discovery.Agency)
	out := make(chan discovery.Result, len(agencies))

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				out <- discovery.Detect(ctx, f, a)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, a := range agencies {
			select {
			case jobs <- a:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() { wg.Wait(); close(out) }()

	results := make([]discovery.Result, 0, len(agencies))
	done := 0
	for r := range out {
		results = append(results, r)
		done++
		fmt.Printf("\r  %d/%d  %-42s", done, len(agencies), truncate(r.Agency.Name, 42))
	}
	fmt.Printf("\r%-60s\r", "")
	return results
}

func writeResults(dir string, results []discovery.Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	jf, err := os.Create(filepath.Join(dir, "scan.json"))
	if err != nil {
		return err
	}
	defer jf.Close()
	enc := json.NewEncoder(jf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return err
	}

	cf, err := os.Create(filepath.Join(dir, "scan.csv"))
	if err != nil {
		return err
	}
	defer cf.Close()
	w := csv.NewWriter(cf)
	defer w.Flush()
	if err := w.Write([]string{
		"name", "domain", "engine", "developer", "reachable", "status",
		"has_inventory", "requires_js", "listing_hits", "tera_token",
		"property_pages", "api_candidates", "sitemap", "robots_disallowed", "error",
	}); err != nil {
		return err
	}
	for _, r := range results {
		if err := w.Write([]string{
			r.Agency.Name, r.Agency.Domain, string(r.Engine), r.Developer,
			strconv.FormatBool(r.Reachable), strconv.Itoa(r.StatusCode),
			strconv.FormatBool(r.HasInventory), strconv.FormatBool(r.RequiresJavaScript),
			strconv.Itoa(r.ListingHits), r.TeraToken,
			strings.Join(r.PropertyPages, " "), strings.Join(r.APICandidates, " "),
			strings.Join(r.Sitemaps, " "),
			strconv.FormatBool(r.RobotsDisallowed), r.Error,
		}); err != nil {
			return err
		}
	}
	return nil
}

func printSummary(results []discovery.Result) {
	byEngine := map[discovery.Engine]int{}
	var reachable, inventory, needsJS, broken, teraTokens int

	for _, r := range results {
		byEngine[r.Engine]++
		switch {
		case !r.Reachable:
			broken++
		default:
			reachable++
		}
		if r.HasInventory {
			inventory++
		}
		if r.RequiresJavaScript {
			needsJS++
		}
		if r.TeraToken != "" {
			teraTokens++
		}
	}

	fmt.Printf("%d inmobiliarias\n\n", len(results))

	engines := make([]discovery.Engine, 0, len(byEngine))
	for e := range byEngine {
		engines = append(engines, e)
	}
	sort.Slice(engines, func(i, j int) bool { return byEngine[engines[i]] > byEngine[engines[j]] })
	for _, e := range engines {
		fmt.Printf("  %-14s %3d\n", e, byEngine[e])
	}

	fmt.Printf("\n  %-14s %3d\n", "alcanzables", reachable)
	fmt.Printf("  %-14s %3d\n", "sin responder", broken)
	fmt.Printf("  %-14s %3d\n", "con inventario", inventory)
	fmt.Printf("  %-14s %3d\n", "requiere JS", needsJS)
	fmt.Printf("  %-14s %3d\n", "tera token", teraTokens)

	// The families worth writing a scraper for, biggest first.
	fmt.Printf("\ncobertura por adaptador (inmobiliarias con inventario server-side):\n")
	cover := map[discovery.Engine]int{}
	for _, r := range results {
		if r.HasInventory && !r.RequiresJavaScript {
			cover[r.Engine]++
		}
	}
	keys := make([]discovery.Engine, 0, len(cover))
	for e := range cover {
		keys = append(keys, e)
	}
	sort.Slice(keys, func(i, j int) bool { return cover[keys[i]] > cover[keys[j]] })
	for _, e := range keys {
		fmt.Printf("  %-14s %3d\n", e, cover[e])
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
