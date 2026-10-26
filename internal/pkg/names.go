// Package pkg holds the rules for package names, versions, .c3l archives and READMEs.
package pkg

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	nameRe       = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	tagRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	c3VersionRe  = regexp.MustCompile(`^\d{1,4}\.\d{1,4}(\.\d{1,4})?$`)
	targetNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

const (
	MaxDescription = 280
	MaxTags        = 5
	MaxLicense     = 40
	MaxURL         = 300
)

var reservedNames = map[string]bool{
	"std": true, "core": true, "c3": true, "c3c": true, "c3pkg": true, "test": true, "tests": true,
	"admin": true, "api": true, "index": true, "www": true, "static": true, "new": true,
}

// ValidateName returns a user-facing problem with a package name, or "" if it is fine.
func ValidateName(name string) string {
	switch {
	case !nameRe.MatchString(name):
		return "Package names are 2–64 characters: lowercase letters, digits, '_' and '-', starting with a letter."
	case reservedNames[name]:
		return "That name is reserved."
	}
	return ""
}

// NameKey folds '-' and '_' together so look-alike names cannot both be registered.
func NameKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "-", "_")
}

// NormalizeTags splits a comma/space separated list into unique valid tags.
func NormalizeTags(raw string) (tags []string, problem string) {
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if !tagRe.MatchString(t) {
			return nil, "Tags use lowercase letters, digits and '-' (up to 24 characters): " + t
		}
		if !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	if len(tags) > MaxTags {
		return nil, "Use at most 5 tags."
	}
	return tags, ""
}

// ValidateURL accepts empty or an absolute http(s) URL.
func ValidateURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || len(s) > MaxURL || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "Enter a full http(s) URL (up to 300 characters)."
	}
	return ""
}

// ValidateC3Version checks the "supported C3 version" field, e.g. 0.7 or 0.7.5.
func ValidateC3Version(s string) string {
	if !c3VersionRe.MatchString(s) {
		return "Enter a C3 version such as 0.7.5."
	}
	return ""
}
