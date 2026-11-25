package mtfmacd

import (
	"testing"
	"time"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
	"github.com/matinnak/crypto-signal-bot/internal/market"
)

func TestNoMissedSignalsWhileFlat(t *testing.T) {
	for _, trend := range []float64{25, -25} {
		h1 := synthetic(3000, time.Hour, trend/3)
		bars := market.Aggregate(h1, 3*time.Hour)
		h4 := market.Aggregate(h1, 4*time.Hour)
		d1 := market.Aggregate(h1, 24*time.Hour)
		res := Run(Default(), bars, h4, d1)
		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		macd, sig, _ := ind.MACD(closes, 12, 26, 9)
		htfC, htfE := htfValues(bars, h4, 4*time.Hour, 50)
		inPos := make([]bool, len(bars)) // position open at bar close
		idx := map[time.Time]int{}
		for i, b := range bars {
			idx[b.OpenTime] = i
		}
		start := -1
		sigAt := map[int]bool{}
		for _, e := range res.Events {
			i := idx[e.Bar.OpenTime]
			switch e.Kind {
			case Signal:
				start = i
				sigAt[i] = true
			case Exit:
				for k := start + 1; k < i; k++ {
					inPos[k] = true
				}
				start = -1
			}
		}
		if start >= 0 {
			for k := start + 1; k < len(bars); k++ {
				inPos[k] = true
			}
		}
		cond, missed := 0, 0
		for i := 1; i < len(bars); i++ {
			bull := macd[i] > sig[i] && macd[i-1] <= sig[i-1] && htfC[i] > htfE[i]
			bear := macd[i] < sig[i] && macd[i-1] >= sig[i-1] && htfC[i] < htfE[i]
			if bull || bear {
				cond++
				if !inPos[i] && !sigAt[i] {
					missed++
					t.Errorf("trend %v: conditions met while flat at bar %d but no signal", trend, i)
				}
			}
		}
		t.Logf("trend %v: %d bars with conditions, %d signals", trend, cond, len(sigAt))
	}
}
