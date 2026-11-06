package app

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validAccountTags(tags []string) bool {
	if len(tags) > 16 {
		return false
	}
	for i, tag := range tags {
		if tag == "" || tag != strings.TrimSpace(tag) || !utf8.ValidString(tag) || len(tag) > 48 || strings.ContainsAny(tag, ",，") || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return false
		}
		for _, prior := range tags[:i] {
			if strings.EqualFold(prior, tag) {
				return false
			}
		}
	}
	return true
}
func validateAccountTagsDocument(doc storeDocument) error {
	check := func(items []storedAccount) error {
		for _, item := range items {
			if !validAccountTags(item.Tags) || (doc.Version < 4 && len(item.Tags) > 0) {
				return errors.New("invalid account tags")
			}
		}
		return nil
	}
	for _, items := range doc.Accounts {
		if err := check(items); err != nil {
			return err
		}
	}
	for _, team := range doc.Teams {
		if err := check(team.Accounts); err != nil {
			return err
		}
	}
	return nil
}
