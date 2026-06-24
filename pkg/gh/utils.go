package gh

import "strings"

// normalizeHunkLine normalizes a diff line (prefixed with + or -) so that
// cosmetic differences don't produce different hashes. Leading whitespace after
// the +/- marker is always stripped. For YAML files, a single leading "- " YAML
// sequence marker is also stripped, so the same change hashes identically
// whether or not it sits on a list-item line (e.g. "      - uses: x" vs
// "        uses: x").
func normalizeHunkLine(line, filename string) string {
	content := strings.TrimLeft(line[1:], " \t")
	if isYAMLFile(filename) {
		// Strip a single leading YAML sequence marker ("-" followed by
		// whitespace) so list-item and non-list-item forms hash alike.
		if rest, ok := strings.CutPrefix(content, "-"); ok {
			if trimmed := strings.TrimLeft(rest, " \t"); trimmed != rest {
				content = trimmed
			}
		}
	}
	return line[:1] + content
}

func isYAMLFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func cleanDependabotMessage(input string) string {
	withoutHtml := removeHtmlTags(input)
	cleaned := removeDependabotTrailingCommand(withoutHtml)
	return removeMultipleNewlines(cleaned)
}

func removeHtmlTags(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	inTag := false
	for _, char := range input {
		switch {
		case char == '<':
			inTag = true
		case char == '>':
			inTag = false
		case !inTag:
			b.WriteRune(char)
		}
	}
	return b.String()
}

func removeDependabotTrailingCommand(input string) string {
	if strings.TrimSpace(input) == "" {
		return input
	}

	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		trim := strings.TrimSpace(l)
		low := strings.ToLower(trim)

		if strings.Contains(low, "dependabot commands and options") ||
			strings.Contains(low, "you can trigger dependabot actions by commenting on this pr") ||
			strings.HasPrefix(low, "- `@dependabot") ||
			strings.HasPrefix(low, "`@dependabot") ||
			strings.HasPrefix(trim, "@dependabot") {
			for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
				out = out[:len(out)-1]
			}
			return strings.Join(out, "\n")
		}

		if strings.Contains(low, "dependabot") {
			lookAhead := strings.ToLower(strings.Join(lines[i:min(i+8, len(lines))], " \n "))
			if strings.Contains(lookAhead, "@dependabot") || strings.Contains(lookAhead, "rebase") || strings.Contains(lookAhead, "recreate") || strings.Contains(lookAhead, "merge") {
				for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1]
				}
				return strings.Join(out, "\n")
			}
		}

		out = append(out, lines[i])
	}

	return strings.TrimRightFunc(strings.Join(out, "\n"), func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}

func removeMultipleNewlines(input string) string {
	var b strings.Builder
	newlineCount := 0
	for _, char := range input {
		if char == '\n' {
			newlineCount++
			if newlineCount <= 2 {
				b.WriteRune(char)
			}
		} else {
			b.WriteRune(char)
			newlineCount = 0
		}
	}
	return b.String()
}
