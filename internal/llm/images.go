package llm

import "strings"

// ImageMediaType guesses the media type of a base64-encoded image from its
// first bytes. Images are PNG unless they look like another format.
func ImageMediaType(b64 string) string {
	switch {
	case strings.HasPrefix(b64, "/9j/"):
		return "image/jpeg"
	case strings.HasPrefix(b64, "R0lGOD"):
		return "image/gif"
	case strings.HasPrefix(b64, "UklGR"):
		return "image/webp"
	}
	return "image/png"
}

// ImageCapable is implemented by providers that can pass images to the model.
// Providers that do not implement it are treated as text-only.
type ImageCapable interface {
	SupportsImages() bool
}

// SupportsImages reports whether p can pass images to the model.
func SupportsImages(p Provider) bool {
	ic, ok := p.(ImageCapable)
	return ok && ic.SupportsImages()
}
