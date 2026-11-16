package model

import "math/big"

var ReferenceFXCNY = map[string]int64{
	"CNY": 1000000, "USD": 7100000, "EUR": 7800000, "GBP": 9000000, "JPY": 48000,
	"CAD": 5200000, "HKD": 910000, "SGD": 5300000, "TWD": 220000, "AUD": 4700000,
	"KRW": 5200, "RUB": 79000, "CHF": 8000000,
}

func RoundRatio(numerator, denominator *big.Int) (int64, bool) {
	if denominator.Sign() <= 0 {
		return 0, false
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(new(big.Int).Abs(remainder), 1).Cmp(denominator) >= 0 {
		if numerator.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	return quotient.Int64(), quotient.IsInt64()
}

func ConvertReferenceMinor(amount int64, from, to string) (int64, bool) {
	source, sourceOK := ReferenceFXCNY[from]
	target, targetOK := ReferenceFXCNY[to]
	if !sourceOK || !targetOK {
		return 0, false
	}
	return RoundRatio(new(big.Int).Mul(big.NewInt(amount), big.NewInt(source)), big.NewInt(target))
}

func ConvertReferenceTotals(amounts map[string]int64, to string) (int64, bool) {
	target, ok := ReferenceFXCNY[to]
	if !ok {
		return 0, false
	}
	total := new(big.Int)
	for from, amount := range amounts {
		rate, ok := ReferenceFXCNY[from]
		if !ok {
			return 0, false
		}
		total.Add(total, new(big.Int).Mul(big.NewInt(amount), big.NewInt(rate)))
	}
	return RoundRatio(total, big.NewInt(target))
}
