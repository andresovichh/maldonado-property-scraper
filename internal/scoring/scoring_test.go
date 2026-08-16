package scoring

import (
	"testing"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

func listing(price float64, beds, baths int, service, radiant bool, op string) *model.Listing {
	l := &model.Listing{
		Operation: model.Str(op),
		Price:     model.Float(price),
		Currency:  model.Str("USD"),
		Bedrooms:  model.Int(beds),
		Bathrooms: model.Int(baths),
	}
	if service {
		l.ServiceRoom = model.Bool(true)
	}
	if radiant {
		l.RadiantHeating = model.Bool(true)
	}
	return l
}

// The brief, restated as a test: a pricier house with the features must beat a
// cheaper one without them. A hard price filter would never have shown it.
func TestBetterHouseOverBudgetBeatsCheaperBareOne(t *testing.T) {
	rich := listing(3100, 3, 3, true, true, model.OperationRentAnnual)
	bare := listing(2700, 3, 3, false, false, model.OperationRentAnnual)

	got := Rank(DefaultProfile(), []*model.Listing{bare, rich})
	if got[0].Listing != rich {
		t.Errorf("USD 3.100 con dependencia y losa debería ganar; ganó %v con %d pts (el otro %d)",
			*got[0].Listing.Price, got[0].Points, got[1].Points)
	}
}

func TestSeasonalIsPushedDown(t *testing.T) {
	season := listing(2500, 3, 3, true, true, model.OperationRentSeason)
	annual := listing(3400, 2, 2, false, false, model.OperationRentAnnual)

	got := Rank(DefaultProfile(), []*model.Listing{season, annual})
	if got[0].Listing != annual {
		t.Error("una temporada perfecta no puede ganarle a un alquiler anual mediocre")
	}
}

func TestImplausiblePriceDoesNotWin(t *testing.T) {
	// Real listing: "Alq. Anual (Dólares): USD 200" for a 3-bedroom house.
	junk := listing(200, 3, 2, true, false, model.OperationRentAnnual)
	real := listing(2400, 3, 3, false, false, model.OperationRentAnnual)

	got := Rank(DefaultProfile(), []*model.Listing{junk, real})
	if got[0].Listing != real {
		t.Errorf("USD 200 no debería ganar: %d vs %d pts", got[0].Points, got[1].Points)
	}
	// …but it is still in the results, with the reason spelled out.
	var found bool
	for _, s := range got {
		if s.Listing == junk {
			found = true
			for _, r := range s.Reasons {
				if len(r) > 0 && r[0] == '-' {
					goto ok
				}
			}
			t.Error("debería explicar por qué el precio es sospechoso")
		ok:
		}
	}
	if !found {
		t.Error("el listing sospechoso no debe desaparecer, sólo bajar")
	}
}

func TestUnstatedFeatureScoresNothingButDoesNotPenalise(t *testing.T) {
	silent := listing(2500, 3, 3, false, false, model.OperationRentAnnual)
	silent.ServiceRoom = nil // the ad simply does not say
	stated := listing(2500, 3, 3, false, false, model.OperationRentAnnual)
	stated.ServiceRoom = model.Bool(false) // the ad says there is none

	a := Rank(DefaultProfile(), []*model.Listing{silent})[0]
	b := Rank(DefaultProfile(), []*model.Listing{stated})[0]
	if a.Points != b.Points {
		t.Errorf("silencio y 'no tiene' deberían puntuar igual por ahora: %d vs %d", a.Points, b.Points)
	}
}
