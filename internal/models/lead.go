package models

import (
	"regexp"
	"strings"
)

// LeadMinWords: when a post's first paragraph has fewer words than this,
// the automatic excerpt takes the second paragraph as well.
const LeadMinWords = 30

var (
	atxHeadingPattern   = regexp.MustCompile(`^\s{0,3}#{1,6}(\s|$)`)
	setextLinePattern   = regexp.MustCompile(`^\s{0,3}(=+|-+)\s*$`)
	ruleLinePattern     = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	tableDelimPattern   = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	referenceDefPattern = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*\S`)
	indentedCodePattern = regexp.MustCompile(`^(    |\t)`)
)

// LeadParagraphs returns a post's opening paragraph as markdown, plus the
// next one when the first is shorter than LeadMinWords. Headings, images
// on their own, rules, tables and code are passed over; a spoiler block
// counts as a paragraph without words (cards show it collapsed). Link
// reference definitions are kept so links in the lead still work. Returns
// "" when the text has no paragraph.
func LeadParagraphs(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	var lead, refs []string
	words := 0
	for _, b := range markdownBlocks(md) {
		if len(lead) == 2 || (len(lead) == 1 && words >= LeadMinWords) {
			// The lead is complete; only reference definitions are still
			// wanted, so skip rendering the rest.
			if isRefBlock(b) {
				refs = append(refs, b)
			}
			continue
		}
		kind, n := classifyBlock(b)
		switch kind {
		case blockRefs:
			refs = append(refs, b)
		case blockText:
			lead = append(lead, b)
			words += n
		}
	}
	if len(lead) == 0 {
		return ""
	}
	return strings.Join(append(lead, refs...), "\n\n")
}

type blockKind int

const (
	blockText blockKind = iota
	blockSkip
	blockRefs
)

// classifyBlock says what a block is and, for text, how many words it has.
func classifyBlock(b string) (blockKind, int) {
	lines := strings.Split(b, "\n")
	first := lines[0]
	switch {
	case spoilerOpenPattern.MatchString(first):
		return blockText, 0
	case fencePattern.MatchString(first),
		atxHeadingPattern.MatchString(first),
		len(lines) == 1 && ruleLinePattern.MatchString(first),
		len(lines) > 1 && setextLinePattern.MatchString(lines[len(lines)-1]),
		len(lines) > 1 && strings.Contains(first, "|") && tableDelimPattern.MatchString(lines[1]):
		return blockSkip, 0
	}
	if isRefBlock(b) {
		return blockRefs, 0
	}
	allIndented := true
	for _, l := range lines {
		allIndented = allIndented && indentedCodePattern.MatchString(l)
	}
	if allIndented {
		return blockSkip, 0
	}
	text, err := PlainTextFromMarkdown(b)
	if err != nil {
		return blockSkip, 0
	}
	n := len(strings.Fields(text))
	if n == 0 { // e.g. an image on its own
		return blockSkip, 0
	}
	return blockText, n
}

// isRefBlock reports whether a block is only link reference definitions.
func isRefBlock(b string) bool {
	for _, l := range strings.Split(b, "\n") {
		if !referenceDefPattern.MatchString(l) {
			return false
		}
	}
	return true
}

// markdownBlocks splits markdown at blank lines, keeping fenced code and
// spoiler blocks (which may contain blank lines) whole.
func markdownBlocks(md string) []string {
	var blocks, cur []string
	inFence := false
	depth := 0
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if fencePattern.MatchString(line) {
			inFence = !inFence
		} else if !inFence {
			if spoilerOpenPattern.MatchString(line) {
				if depth == 0 {
					flush() // a spoiler starts its own block
				}
				depth++
			} else if depth > 0 && spoilerClosePattern.MatchString(line) {
				depth--
				if depth == 0 {
					cur = append(cur, line)
					flush()
					continue
				}
			}
		}
		if strings.TrimSpace(line) == "" && !inFence && depth == 0 {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}
