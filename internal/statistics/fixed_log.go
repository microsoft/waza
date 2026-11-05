package statistics

import (
	"fmt"
	"math"
	"math/big"
)

func pairedFactorUpper(alpha float64, comparisons int) (*big.Rat, error) {
	if !isFinite(alpha) || alpha <= 0 || alpha >= 1 {
		return nil, fmt.Errorf("family error probability alpha must be finite and in (0, 1), got %v", alpha)
	}
	if comparisons < 1 {
		return nil, fmt.Errorf("declare at least one comparison in the fixed family, got %d", comparisons)
	}
	fraction, exponent := math.Frexp(alpha)
	alphaLower, _ := logMantissaInterval(new(big.Rat).SetFloat64(2 * fraction))
	family := big.NewInt(int64(comparisons))
	familyExponent := family.BitLen() - 1
	familyMantissa := new(big.Rat).SetFrac(family, new(big.Int).Lsh(big.NewInt(1), uint(familyExponent)))
	_, familyUpper := logMantissaInterval(familyMantissa)
	_, twoUpper := logMantissaInterval(big.NewRat(2, 1))
	coefficient := int64(1 + familyExponent - (exponent - 1))
	factor := new(big.Rat).Mul(big.NewRat(coefficient, 1), twoUpper)
	factor.Add(factor, familyUpper)
	factor.Sub(factor, alphaLower)
	return factor.Mul(factor, big.NewRat(2, 1)), nil
}

// For 1 <= m <= 2, y=(m-1)/(m+1) <= 1/3. The positive
// atanh series has a remainder <= 2*y^(2N+1)/((2N+1)*(1-y²)).
func logMantissaInterval(m *big.Rat) (*big.Rat, *big.Rat) {
	y := new(big.Rat).Quo(
		new(big.Rat).Sub(m, big.NewRat(1, 1)),
		new(big.Rat).Add(m, big.NewRat(1, 1)),
	)
	squared := new(big.Rat).Mul(y, y)
	power := new(big.Rat).Set(y)
	lower := new(big.Rat)
	const terms = 40
	for k := range terms {
		term := new(big.Rat).Quo(power, big.NewRat(int64(2*k+1), 1))
		lower.Add(lower, term)
		power.Mul(power, squared)
	}
	lower.Mul(lower, big.NewRat(2, 1))
	tail := new(big.Rat).Mul(power, big.NewRat(2, 1))
	tail.Quo(tail, new(big.Rat).Mul(
		big.NewRat(2*terms+1, 1),
		new(big.Rat).Sub(big.NewRat(1, 1), squared),
	))
	return lower, new(big.Rat).Add(lower, tail)
}

func rationalUpperFloat(value *big.Rat) float64 {
	rounded, exact := value.Float64()
	if !exact && new(big.Rat).SetFloat64(rounded).Cmp(value) < 0 {
		return math.Nextafter(rounded, math.Inf(1))
	}
	return rounded
}
