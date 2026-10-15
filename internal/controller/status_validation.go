package controller

import (
	"errors"
	"strings"
	"unicode"
)

func validateStatusText(report StatusReport) error {
	if len(report.Status.BinaryVersion) > 128 || len(report.Status.InstanceID) > 128 || len(report.Status.FailureCounters) > 128 {
		return errors.New("status metadata exceeds limit")
	}
	for key := range report.Status.FailureCounters {
		if len(key) > 128 {
			return errors.New("status counter name exceeds limit")
		}
	}
	texts := []string{report.Status.LastError, report.Status.ApplyError, report.Status.XrayError}
	for _, link := range report.Links {
		texts = append(texts, link.LastError)
	}
	for _, grant := range report.Grants {
		texts = append(texts, grant.LastError)
	}
	for _, text := range texts {
		if len(text) > 1024 || strings.ContainsFunc(text, func(r rune) bool {
			return (unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r') || unicode.In(r, unicode.Bidi_Control)
		}) {
			return errors.New("status text must be at most 1024 bytes and contain no hidden control characters")
		}
	}
	return nil
}
