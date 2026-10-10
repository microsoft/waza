package releasepolicy

import (
	"encoding/json"
	"math/big"
	"testing"
)

func TestNumericTokensDoNotDefaultFromStrings(t *testing.T) {
	p := testPolicy(t, 8)
	object, err := objectJSON(testBytes(t, p))
	if err != nil {
		t.Fatal(err)
	}
	design, ok := object["design"].(map[string]any)
	if !ok {
		t.Fatal("missing test design")
	}
	design["alpha"] = "0.05"
	data, err := SealJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePolicy(data); err == nil {
		t.Fatal("string silently became an exact JSON numeric token")
	}
}

func TestExactNumericRangeAndPrecision(t *testing.T) {
	for _, token := range []json.Number{"1.0000000000000000000000000001", "-0.0000000000000000000000000001"} {
		if _, err := number(token, "boundary", 0, 1); err == nil {
			t.Fatalf("float-rounded out-of-range token admitted: %s", token)
		}
	}
	p := testPolicy(t, 8)
	directory := collectTest(t, p, testCollector(p))
	d, err := ReadDecision(directory)
	if err != nil || d.HalfWidth == nil {
		t.Fatalf("missing allocation-only precision radius: %+v %v", d, err)
	}
	// Select a fresh test design whose declared precision is below the same
	// allocation-only radius but rounds to that float64. No outcomes select it.
	exact := new(big.Rat).SetFloat64(*d.HalfWidth)
	epsilon, ok := new(big.Rat).SetString("0.000000000000000000000000000001")
	if !ok {
		t.Fatal("invalid test epsilon")
	}
	exact.Sub(exact, epsilon)
	p.Design.MaximumHalfWidth = json.Number(exact.FloatString(80))
	p = sealTestPolicy(t, p)
	directory = collectTest(t, p, testCollector(p))
	d, err = ReadDecision(directory)
	if err != nil || d.Accepted || d.Statistics.State != "underpowered" {
		t.Fatalf("rounded-up precision threshold strict-passed: %+v %v", d, err)
	}
}
