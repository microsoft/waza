package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
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

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check() (result error) {
	for _, path := range []string{"web", "web/dist"} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("release dashboard directory %s: %w", path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("release dashboard directory must not be a symlink: %s", path)
		}
	}
	root, err := os.OpenRoot("web/dist")
	if err != nil {
		return fmt.Errorf("opening release dashboard: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("closing release dashboard root: %w", err))
		}
	}()
	index, err := readFile(root, "index.html")
	if err != nil {
		return err
	}
	if _, err := readFile(root, "favicon.svg"); err != nil {
		return err
	}
	if err := directory(root, "assets"); err != nil {
		return err
	}
	assets, err := openFile(root, "assets")
	if err != nil {
		return fmt.Errorf("opening release dashboard assets: %w", err)
	}
	entries, readErr := assets.ReadDir(-1)
	if err := errors.Join(readErr, assets.Close()); err != nil {
		return fmt.Errorf("reading release dashboard assets: %w", err)
	}
	hasJS, hasCSS := false, false
	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".js":
			hasJS = true
		case ".css":
			hasCSS = true
		default:
			continue
		}
		if _, err := readFile(root, "assets/"+entry.Name()); err != nil {
			return err
		}
	}
	if !hasJS || !hasCSS {
		return errors.New("release dashboard requires JavaScript and CSS bundles")
	}
	return checkDocument(root, index)
}

func directory(root *os.Root, path string) error {
	info, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("release dashboard directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("release dashboard requires a nonsymlink directory: %s", path)
	}
	return nil
}

func readFile(root *os.Root, path string) (content []byte, result error) {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, `\`) {
			return nil, fmt.Errorf("invalid release dashboard path: %s", path)
		}
	}
	for i := 1; i < len(parts); i++ {
		if err := directory(root, strings.Join(parts[:i], "/")); err != nil {
			return nil, err
		}
	}
	info, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("release dashboard file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("release dashboard requires a nonempty regular nonsymlink file: %s", path)
	}
	file, err := openFile(root, path)
	if err != nil {
		return nil, fmt.Errorf("opening release dashboard file %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("closing release dashboard file %s: %w", path, err))
		}
	}()
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting release dashboard file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("release dashboard file is no longer nonempty and regular: %s", path)
	}
	content, err = io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("reading release dashboard file %s: %w", path, err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("release dashboard file became empty: %s", path)
	}
	return content, nil
}

func checkDocument(root *os.Root, index []byte) error {
	tokenizer := html.NewTokenizer(bytes.NewReader(index))
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			// HTML tokenization removes duplicates, so inspect the original tag first.
			if err := uniqueAttributes(tokenizer.Raw()); err != nil {
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
	hasEntry, hasStyle := false, false
	err = walk(document, func(node *html.Node, attrs map[string]string) error {
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
		if _, err := readFile(root, strings.TrimPrefix(resolved.Path, "/")); err != nil {
			return err
		}
		scriptType, hasType := attrs["type"]
		if !hasType && attrs["language"] != "" {
			scriptType = "text/" + attrs["language"]
		}
		scriptType = asciiLower(strings.Trim(scriptType, htmlWhitespace))
		executable := slices.Contains([]string{"", "module", "text/javascript", "application/javascript", "text/ecmascript", "application/ecmascript"},
			scriptType)
		_, nomodule := attrs["nomodule"]
		_, disabled := attrs["disabled"]
		if node.Data == "script" && strings.HasSuffix(resolved.Path, ".js") && executable && (!nomodule || scriptType == "module") {
			hasEntry = true
		}
		styleType := asciiLower(strings.Trim(attrs["type"], htmlWhitespace))
		if node.Data == "link" && slices.Contains(rels, "stylesheet") && !slices.Contains(rels, "alternate") &&
			strings.HasSuffix(resolved.Path, ".css") && !disabled && (styleType == "" || styleType == "text/css") {
			hasStyle = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !hasEntry || !hasStyle {
		return errors.New("release dashboard must load an executable entry script and enabled stylesheet, not only preload or inert content")
	}
	return nil
}

func uniqueAttributes(tag []byte) error {
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
