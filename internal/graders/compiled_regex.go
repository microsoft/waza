package graders

import "regexp"

type compiledRegex struct {
	source string
	regexp *regexp.Regexp
	err    error
}

func compileRegex(source string) compiledRegex {
	re, err := regexp.Compile(source)
	return compiledRegex{source: source, regexp: re, err: err}
}

func compileRegexes(sources []string) []compiledRegex {
	compiled := make([]compiledRegex, len(sources))
	for i, source := range sources {
		compiled[i] = compileRegex(source)
	}
	return compiled
}

func regexSources(compiled []compiledRegex) []string {
	sources := make([]string, len(compiled))
	for i, pattern := range compiled {
		sources[i] = pattern.source
	}
	return sources
}
