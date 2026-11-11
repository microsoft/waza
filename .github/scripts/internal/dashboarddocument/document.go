package dashboarddocument

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

const htmlWhitespace = " \t\n\r\f"

func asciiLower(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

// Check validates HTML semantics using only the caller's admitted asset reader.
func Check(index []byte, readAsset func(string) error) error {
	return check(index, readAsset, true)
}

// CheckReferences preserves layout-only callers while sharing DOM and URL admission.
func CheckReferences(index []byte, readAsset func(string) error) error {
	return check(index, readAsset, false)
}

func check(index []byte, readAsset func(string) error, requireActive bool) error {
	tokenizer := html.NewTokenizer(bytes.NewReader(index))
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			// HTML tokenization removes duplicates, so inspect the original tag first.
			if err := UniqueAttributes(tokenizer.Raw()); err != nil {
				return err
			}
		}
	}
	if err := tokenizer.Err(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("tokenizing release dashboard: %w", err)
	}
	if len(bytes.TrimSpace(tokenizer.Raw())) != 0 {
		return errors.New("release dashboard has an unfinished HTML token")
	}
	document, err := html.ParseWithOptions(bytes.NewReader(index), html.ParseOptionEnableScripting(true))
	if err != nil {
		return fmt.Errorf("parsing release dashboard: %w", err)
	}
	base := &url.URL{Scheme: "https", Host: "dashboard.invalid", Path: "/"}
	baseSet := false
	if err := walk(document, func(node *html.Node, attrs map[string]string) error {
		if node.Data == "base" && !baseSet {
			if href, exists := attrs["href"]; exists {
				reference, err := localURL(href)
				if err != nil {
					return err
				}
				base = base.ResolveReference(reference)
				if base.Scheme != "https" || base.Host != "dashboard.invalid" || base.Path != "/" {
					return fmt.Errorf("release dashboard base must preserve the document origin and root: %s", href)
				}
				baseSet = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	hasEntry := false
	preferredStyle := ""
	styleTitles := []string{}
	err = walk(document, func(node *html.Node, attrs map[string]string) error {
		if node.Data == "meta" && asciiLower(attrs["http-equiv"]) == "default-style" && attrs["content"] != "" {
			preferredStyle = attrs["content"]
		}
		inlineType := asciiLower(attrs["type"])
		if node.Data == "style" && (inlineType == "" || inlineType == "text/css") &&
			preferredStyle == "" && attrs["title"] != "" {
			preferredStyle = attrs["title"]
		}
		reference, exists := "", false
		rels := strings.FieldsFunc(asciiLower(attrs["rel"]), func(r rune) bool {
			return strings.ContainsRune(htmlWhitespace, r)
		})
		switch node.Data {
		case "script":
			reference, exists = attrs["src"]
		case "link":
			assetLink := slices.Contains(rels, "stylesheet") || slices.Contains(rels, "modulepreload") ||
				slices.Contains(rels, "preload") ||
				strings.HasPrefix(strings.TrimPrefix(strings.TrimPrefix(attrs["href"], "./"), "/"), "assets/")
			if assetLink {
				reference, exists = attrs["href"], true
			}
		}
		if !exists {
			return nil
		}
		if strings.TrimSpace(reference) == "" {
			return errors.New("release dashboard asset reference must not be empty")
		}
		target, err := localURL(reference)
		if err != nil {
			return err
		}
		resolved := base.ResolveReference(target)
		if !strings.HasPrefix(resolved.Path, "/assets/") {
			return fmt.Errorf("release dashboard reference does not resolve to local assets: %s", reference)
		}
		if err := readAsset(strings.TrimPrefix(resolved.Path, "/")); err != nil {
			return err
		}
		scriptType, hasType := attrs["type"]
		if !hasType && attrs["language"] != "" {
			scriptType = "text/" + attrs["language"]
		}
		if scriptType == "" {
			scriptType = "text/javascript"
		}
		scriptType = asciiLower(scriptType)
		classicType := scriptType
		if hasType {
			classicType = strings.Trim(classicType, htmlWhitespace)
		}
		classic := slices.Contains([]string{"text/javascript", "application/javascript", "text/ecmascript", "application/ecmascript"},
			classicType)
		executable := classic || scriptType == "module"
		forValue, hasFor := attrs["for"]
		event, hasEvent := attrs["event"]
		if classic && hasFor && hasEvent {
			forValue = asciiLower(strings.Trim(forValue, htmlWhitespace))
			event = asciiLower(strings.Trim(event, htmlWhitespace))
			executable = forValue == "window" && (event == "onload" || event == "onload()")
		}
		_, nomodule := attrs["nomodule"]
		_, disabled := attrs["disabled"]
		if node.Data == "script" && strings.HasSuffix(resolved.Path, ".js") && executable && (!nomodule || scriptType == "module") {
			hasEntry = true
		}
		styleType := asciiLower(strings.Trim(attrs["type"], htmlWhitespace))
		media := asciiLower(strings.Trim(attrs["media"], htmlWhitespace))
		if node.Data == "link" && slices.Contains(rels, "stylesheet") && !slices.Contains(rels, "alternate") &&
			!disabled && (styleType == "" || styleType == "text/css") {
			if preferredStyle == "" && attrs["title"] != "" {
				preferredStyle = attrs["title"]
			}
			if strings.HasSuffix(resolved.Path, ".css") && (media == "" || media == "all") {
				styleTitles = append(styleTitles, attrs["title"])
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	hasStyle := slices.Contains(styleTitles, "") || slices.Contains(styleTitles, preferredStyle)
	if requireActive && (!hasEntry || !hasStyle) {
		return errors.New("release dashboard must load an executable entry script and enabled unconditional stylesheet, not only preload or inert content")
	}
	return nil
}

// UniqueAttributes checks the original tag before DOM normalization loses duplicates.
func UniqueAttributes(tag []byte) error {
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
	i := 1
	for i < len(tag) && !space(tag[i]) && tag[i] != '/' && tag[i] != '>' {
		i++
	}
	seen := make(map[string]bool)
	for i < len(tag) {
		for i < len(tag) && (space(tag[i]) || tag[i] == '/') {
			i++
		}
		if i == len(tag) || tag[i] == '>' {
			return nil
		}
		start := i
		for i < len(tag) && !space(tag[i]) && tag[i] != '=' && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		if start == i {
			return errors.New("unsupported release dashboard attribute name")
		}
		key := asciiLower(string(tag[start:i]))
		if seen[key] {
			return fmt.Errorf("duplicate release dashboard attribute: %s", key)
		}
		seen[key] = true
		for i < len(tag) && space(tag[i]) {
			i++
		}
		if i == len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && space(tag[i]) {
			i++
		}
		if i < len(tag) && (tag[i] == '\'' || tag[i] == '"') {
			quote := tag[i]
			i++
			for i < len(tag) && tag[i] != quote {
				i++
			}
			if i == len(tag) {
				return errors.New("unfinished release dashboard attribute value")
			}
			i++
		} else {
			for i < len(tag) && !space(tag[i]) && tag[i] != '>' {
				i++
			}
		}
	}
	return nil
}

func localURL(value string) (*url.URL, error) {
	reference, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid release dashboard URL %q: %w", value, err)
	}
	if reference.IsAbs() || reference.Host != "" || reference.User != nil || strings.HasPrefix(value, "//") {
		return nil, fmt.Errorf("unsupported nonlocal release dashboard URL: %q", value)
	}
	return reference, nil
}

func walk(node *html.Node, visit func(*html.Node, map[string]string) error) error {
	if node.Type == html.ElementNode {
		if node.Namespace == "" && (node.Data == "template" || node.Data == "noscript") {
			return nil
		}
		if node.Namespace != "" {
			if node.Data == "script" || node.Data == "link" || node.Data == "base" {
				return fmt.Errorf("unsupported foreign release dashboard asset element: %s", node.Data)
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if err := walk(child, visit); err != nil {
					return err
				}
			}
			return nil
		}
		attrs := make(map[string]string, len(node.Attr))
		for _, attribute := range node.Attr {
			if _, duplicate := attrs[attribute.Key]; duplicate {
				return fmt.Errorf("duplicate release dashboard attribute: %s", attribute.Key)
			}
			attrs[attribute.Key] = attribute.Val
		}
		if err := visit(node, attrs); err != nil {
			return err
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := walk(child, visit); err != nil {
			return err
		}
	}
	return nil
}
