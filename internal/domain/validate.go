package domain

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	envKeyRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	imageRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-/:@]{0,199}$`)
)

func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return Invalid("Name: 1–40 chars, a-z 0-9 and - only")
	}
	return nil
}

func ValidEnvKey(key string) error {
	if !envKeyRE.MatchString(key) {
		return Invalid("Key: UPPER_SNAKE_CASE only")
	}
	return nil
}

func ValidImage(image string) error {
	if !imageRE.MatchString(image) {
		return Invalid("Image must look like repo/name:tag")
	}
	return nil
}

func ValidPort(port *int) error {
	if port != nil && (*port < 1 || *port > 65535) {
		return Invalid(MsgPortRange)
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(name string) string {
	s, _, _ := transform.String(transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn))), name)
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s[:min(len(s), 40)], "-")
}
