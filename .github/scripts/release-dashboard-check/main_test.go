package main

import (
	"strings"
	"testing"
)

func TestDuplicateAttributesBeforeDOMNormalization(t *testing.T) {
	err := checkDocument(nil, []byte(`<script src="/assets/a.js" src="/assets/missing.js"></script>`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate source attributes must fail before file admission: %v", err)
	}
}

func TestUniqueAttributes(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		fail bool
	}{
		{`<script src=./assets/a.js>`, false},
		{`<link rel=stylesheet href=assets/a.css>`, false},
		{`<script SRC='a' src=b>`, true},
		{`<script src='a'src='b'>`, true},
		{`<script src="a src=b" defer>`, false},
		{`<link disabled href="a?x=1&y=2"/>`, false},
		{"<link rel=stylesheet\fhref=a>", false},
		{`<link href="a" href=b>`, true},
		{`<link href="unfinished>`, true},
		{`<link =broken>`, true},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			if err := uniqueAttributes([]byte(tc.tag)); (err != nil) != tc.fail {
				t.Fatalf("raw duplicate-key admission: %v", err)
			}
		})
	}
}
