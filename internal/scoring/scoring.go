// Package scoring ranks listings against a search profile.
//
// The point is that nothing sensible gets discarded for being slightly off. A
// USD 3.100 house with three bedrooms, a service room and radiant heating should
// beat a USD 2.700 one with none of that — a hard price filter would never show it.
package scoring

import (
	"fmt"
	"math"
	"sort"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

// Profile is what the buyer is looking for. Every weight is data, not code, so the
// ranking can be tuned without touching this file.
type Profile struct {
	Operation    string // model.OperationRentAnnual
	PropertyType string // model.TypeHouse

	PriceMin     float64 // ideal band, lower bound
	PriceMax     float64 // ideal band, upper bound
	PriceSoftMax float64 // beyond this, stop bothering
	// PriceFloor guards against data-entry noise. Real listings carry things like
	// "Alq. Anual (Dólares): USD 200" for a 3-bedroom house — placeholder or typo,
	// either way it should not win a cheap-first ranking. The listing is still
	// shown and still says why, it just stops scoring like a bargain.
	PriceFloor float64

	BedroomsTarget  int
	BathroomsTarget int

	PreferServiceRoom bool
	PreferServiceBath bool
	PreferRadiantHeat bool

	W Weights
}

// Weights are the points each fact is worth.
type Weights struct {
	OperationMatch int
	PriceInBand    int
	// PriceOverSoft is where the price score lands at the soft max. Between
	// PriceMax and PriceSoftMax it slides linearly from PriceInBand down to this,
	// instead of dropping off a cliff the moment the band is crossed.
	PriceOverSoft  int
	BedroomsExact  int
	BedroomsOff1   int // penalty per bedroom off target
	BathroomsExact int
	BathroomsOff1  int
	ServiceRoom    int
	ServiceBath    int
	RadiantHeating int
	SeasonalOnly   int // penalty: the listing is temporada, not an annual rent
	UnknownPrice   int // penalty: we could not find an annual price at all
}

// DefaultWeights encodes the brief: annual rental and radiant heating matter a lot,
// a bedroom too many barely matters, temporada is disqualifying in practice.
func DefaultWeights() Weights {
	return Weights{
		OperationMatch: 25,
		PriceInBand:    30,
		PriceOverSoft:  -30,
		BedroomsExact:  15,
		BedroomsOff1:   -4,
		BathroomsExact: 10,
		BathroomsOff1:  -5,
		ServiceRoom:    10,
		ServiceBath:    5,
		RadiantHeating: 15,
		SeasonalOnly:   -100,
		UnknownPrice:   -20,
	}
}

// DefaultProfile is the brief: a house for annual rent in Maldonado, ~3 bedrooms,
// ~3 bathrooms, USD 2.000–3.000, service quarters and radiant heating desirable.
func DefaultProfile() Profile {
	return Profile{
		Operation:         model.OperationRentAnnual,
		PropertyType:      model.TypeHouse,
		PriceMin:          2000,
		PriceMax:          3000,
		PriceSoftMax:      3500,
		PriceFloor:        400,
		BedroomsTarget:    3,
		BathroomsTarget:   3,
		PreferServiceRoom: true,
		PreferServiceBath: true,
		PreferRadiantHeat: true,
		W:                 DefaultWeights(),
	}
}

// Score is a ranked listing plus why it ranked there.
type Score struct {
	Listing *model.Listing `json:"listing"`
	Points  int            `json:"points"`
	Percent int            `json:"percent"`
	Reasons []string       `json:"reasons"`
}

// maxPoints is the best a listing could possibly do, used to turn points into the
// percentage the CLI shows.
func (p Profile) maxPoints() int {
	m := p.W.OperationMatch + p.W.PriceInBand + p.W.BedroomsExact + p.W.BathroomsExact
	if p.PreferServiceRoom {
		m += p.W.ServiceRoom
	}
	if p.PreferServiceBath {
		m += p.W.ServiceBath
	}
	if p.PreferRadiantHeat {
		m += p.W.RadiantHeating
	}
	return m
}

// Rank scores every listing and returns them best first.
func Rank(p Profile, listings []*model.Listing) []Score {
	out := make([]Score, 0, len(listings))
	for _, l := range listings {
		out = append(out, p.score(l))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Points > out[j].Points })
	return out
}

func (p Profile) score(l *model.Listing) Score {
	s := Score{Listing: l}
	add := func(pts int, format string, args ...any) {
		if pts == 0 {
			return
		}
		s.Points += pts
		s.Reasons = append(s.Reasons, fmt.Sprintf("%+d %s", pts, fmt.Sprintf(format, args...)))
	}

	// --- operation --------------------------------------------------------
	switch {
	case l.Operation == nil:
		add(p.W.UnknownPrice, "operación desconocida")
	case *l.Operation == p.Operation:
		add(p.W.OperationMatch, "alquiler anual confirmado")
	case *l.Operation == model.OperationRentSeason:
		add(p.W.SeasonalOnly, "sólo temporada")
	case *l.Operation == model.OperationRentWinter:
		add(p.W.SeasonalOnly/2, "sólo invernal")
	}

	// --- price ------------------------------------------------------------
	if l.Price == nil || *l.Price <= 0 {
		add(p.W.UnknownPrice, "sin precio anual publicado")
	} else if p.PriceFloor > 0 && *l.Price < p.PriceFloor {
		add(p.W.UnknownPrice, "precio implausible (USD %.0f) — probablemente mal cargado", *l.Price)
	} else {
		add(p.priceScore(*l.Price), "precio USD %.0f", *l.Price)
	}

	// --- rooms ------------------------------------------------------------
	scoreCount(add, l.Bedrooms, p.BedroomsTarget, p.W.BedroomsExact, p.W.BedroomsOff1, "dormitorio")
	scoreCount(add, l.Bathrooms, p.BathroomsTarget, p.W.BathroomsExact, p.W.BathroomsOff1, "baño")

	// --- features ---------------------------------------------------------
	// Only a confirmed true scores. A nil means the ad is silent, and silence is
	// not evidence of absence — it just earns nothing.
	if p.PreferServiceRoom && isTrue(l.ServiceRoom) {
		add(p.W.ServiceRoom, "dependencia de servicio")
	}
	if p.PreferServiceBath && isTrue(l.ServiceBath) {
		add(p.W.ServiceBath, "baño de servicio")
	}
	if p.PreferRadiantHeat && isTrue(l.RadiantHeating) {
		add(p.W.RadiantHeating, "losa radiante")
	}

	if maxP := p.maxPoints(); maxP > 0 {
		s.Percent = int(math.Round(float64(s.Points) / float64(maxP) * 100))
		s.Percent = max(0, min(100, s.Percent))
	}
	return s
}

// priceScore rewards the band fully and then decays linearly to PriceOverSoft at
// the soft max.
//
// The brief asked for a stepped penalty (-5 over the band, -12 further out) AND for
// a USD 3.100 house with a service room and radiant heating to outrank a bare
// USD 2.700 one. Those two cannot both hold: crossing the band cost 35 points in
// one step (the +30 band bonus lost plus the penalty), which no combination of
// features could make up. That is a hard filter wearing a soft filter's clothes,
// so the steps are gone and the slide stays. Pinned by TestBetterHouseOverBudget.
func (p Profile) priceScore(price float64) int {
	switch {
	case price <= p.PriceMax:
		// At or below the band. Cheaper than asked for is not a problem.
		return p.W.PriceInBand
	case price >= p.PriceSoftMax:
		return p.W.PriceOverSoft
	default:
		span := p.PriceSoftMax - p.PriceMax
		if span <= 0 {
			return p.W.PriceOverSoft
		}
		t := (price - p.PriceMax) / span
		return int(math.Round(float64(p.W.PriceInBand) + t*float64(p.W.PriceOverSoft-p.W.PriceInBand)))
	}
}

func scoreCount(add func(int, string, ...any), got *int, target, exact, offPenalty int, noun string) {
	if got == nil {
		return // not stated: no credit, no penalty
	}
	diff := *got - target
	switch {
	case diff == 0:
		add(exact, "%d %ss", *got, noun)
	default:
		if diff < 0 {
			diff = -diff
		}
		add(offPenalty*diff, "%d %ss (objetivo %d)", *got, noun, target)
	}
}

func isTrue(b *bool) bool { return b != nil && *b }
