// Command search ranks the crawled listings against a search profile.
//
//	go run ./cmd/search                       # the brief: casa, anual, 3/3, USD 2-3k
//	go run ./cmd/search -min 2500 -max 3500
//	go run ./cmd/search -why                  # show the points breakdown
//
// Nothing is filtered out for being slightly off — that is the whole point. A
// USD 3.100 house with a service room and radiant heating should outrank a
// USD 2.700 one with neither, and a hard price filter would hide it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
	"github.com/andresovichh/maldonado-property-scraper/internal/scoring"
)

func main() {
	var (
		in       = flag.String("in", "out/listings.json", "listings produced by ./cmd/crawl")
		min      = flag.Float64("min", 2000, "ideal price band, lower bound (USD)")
		max      = flag.Float64("max", 3000, "ideal price band, upper bound (USD)")
		softMax  = flag.Float64("soft-max", 3500, "beyond this the listing is heavily penalised")
		bedrooms = flag.Int("bedrooms", 3, "target bedrooms")
		baths    = flag.Int("bathrooms", 3, "target bathrooms")
		top      = flag.Int("top", 20, "how many results to print")
		why      = flag.Bool("why", false, "show the points breakdown per listing")
		all      = flag.Bool("all", false, "include listings with no annual price (temporada, etc.)")
	)
	flag.Parse()

	b, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v (¿corriste ./cmd/crawl primero?)\n", err)
		os.Exit(1)
	}
	var listings []*model.Listing
	if err := json.Unmarshal(b, &listings); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	p := scoring.DefaultProfile()
	p.PriceMin, p.PriceMax, p.PriceSoftMax = *min, *max, *softMax
	p.BedroomsTarget, p.BathroomsTarget = *bedrooms, *baths

	ranked := scoring.Rank(p, listings)

	fmt.Printf("%d listings · perfil: casa en alquiler anual, USD %.0f–%.0f (máx %.0f), %d dorm / %d baños\n\n",
		len(listings), p.PriceMin, p.PriceMax, p.PriceSoftMax, p.BedroomsTarget, p.BathroomsTarget)

	fmt.Printf("%-5s %-8s %-5s %-5s %-4s %-4s %-22s %s\n",
		"SCORE", "PRECIO", "DORM", "BAÑOS", "LOSA", "SERV", "INMOBILIARIA", "URL")
	fmt.Println(strings.Repeat("─", 118))

	shown := 0
	for _, s := range ranked {
		if shown >= *top {
			break
		}
		l := s.Listing
		// By default only show things actually offered for annual rent; the price
		// table is what decides that, not the URL the listing sat under.
		if !*all && (l.Operation == nil || *l.Operation != model.OperationRentAnnual) {
			continue
		}
		shown++

		fmt.Printf("%-5s %-8s %-5s %-5s %-4s %-4s %-22s %s\n",
			fmt.Sprintf("%d%%", s.Percent),
			money(l.Currency, l.Price),
			num(l.Bedrooms), num(l.Bathrooms),
			yn(l.RadiantHeating), yn(l.ServiceRoom),
			trunc(l.AgencyDomain, 22), l.URL)

		if *why {
			for _, r := range s.Reasons {
				fmt.Printf("        %s\n", r)
			}
			fmt.Println()
		}
	}

	if shown == 0 {
		fmt.Println("(ningún listing con precio anual — probá -all para ver también temporada)")
	}
}

func money(cur *string, v *float64) string {
	if v == nil {
		return "—"
	}
	c := "USD"
	if cur != nil && *cur != "" {
		c = *cur
	}
	if c == "USD" {
		return fmt.Sprintf("%.0f", *v)
	}
	return fmt.Sprintf("%s %.0f", c, *v)
}

func num(v *int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *v)
}

// yn renders the three states honestly: "sí", "no", and "?" for an ad that simply
// never said. Printing "no" for silence would be a lie the ranking already avoids.
func yn(b *bool) string {
	switch {
	case b == nil:
		return "?"
	case *b:
		return "sí"
	default:
		return "no"
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
