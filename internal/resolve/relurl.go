package resolve

import (
	"fmt"
	"net/url"
)

// ResolveRelativeURL resolves a relative reference against a parent URL
// using RFC 3986 semantics. Absolute URLs are returned unchanged. The
// caller must validate the resolved URL against allowed prefixes.
func ResolveRelativeURL(parentURL, relRef string) (string, error) {
	rel, err := url.Parse(relRef)
	if err != nil {
		return "", fmt.Errorf("parsing relative ref %q: %w", relRef, err)
	}
	if rel.IsAbs() {
		return relRef, nil
	}

	parent, err := url.Parse(parentURL)
	if err != nil {
		return "", fmt.Errorf("parsing parent URL %q: %w", parentURL, err)
	}

	resolved := parent.ResolveReference(rel)
	return resolved.String(), nil
}
