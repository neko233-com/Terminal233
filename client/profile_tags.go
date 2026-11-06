package main

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxProfileTags = 16

// Tags are small, displayable labels, not arbitrary host metadata.
func parseProfileTags(input string) ([]string, error) {
	input = strings.ReplaceAll(input, "，", ",")
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	tags := make([]string, 0, maxProfileTags)
	for _, part := range strings.Split(input, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if !utf8.ValidString(tag) || len(tag) > 48 || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return nil, errors.New("标签不能包含控制字符，单个标签最多 48 字节")
		}
		duplicate := false
		for _, existing := range tags {
			if strings.EqualFold(existing, tag) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			tags = append(tags, tag)
		}
		if len(tags) > maxProfileTags {
			return nil, errors.New("每台主机最多 16 个标签")
		}
	}
	return tags, nil
}

func validStoredProfileTags(tags []string) bool {
	if len(tags) > maxProfileTags {
		return false
	}
	for i, tag := range tags {
		if tag == "" || tag != strings.TrimSpace(tag) || !utf8.ValidString(tag) || len(tag) > 48 || strings.ContainsAny(tag, ",，") || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return false
		}
		for _, previous := range tags[:i] {
			if strings.EqualFold(previous, tag) {
				return false
			}
		}
	}
	return true
}
