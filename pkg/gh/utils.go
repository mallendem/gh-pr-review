package gh

import "strings"

func cleanDependabotMessage(input string) string {
	withoutHtml := removeHtmlTags(input)
	cleaned := removeDependabotTrailingCommand(withoutHtml)
	cleaned = removeMultipleNewlines(cleaned)
	return cleaned
}

func removeHtmlTags(input string) string {
	result := ""
	inTag := false
	for _, char := range input {
		if char == '<' {
			inTag = true
			continue
		}
		if char == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result += string(char)
		}
	}
	return result
}

func removeDependabotTrailingCommand(input string) string {
	// If input is empty, return as-is
	if strings.TrimSpace(input) == "" {
		return input
	}

	lines := strings.Split(input, "\n")
	// build output until we detect a Dependabot instruction block
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		trim := strings.TrimSpace(l)
		low := strings.ToLower(trim)

		// Common markers that indicate the start of the Dependabot automated comment block
		if strings.Contains(low, "dependabot commands and options") ||
			strings.Contains(low, "you can trigger dependabot actions by commenting on this pr") ||
			strings.HasPrefix(low, "- `@dependabot") ||
			strings.HasPrefix(low, "`@dependabot") ||
			strings.HasPrefix(trim, "@dependabot") {
			// Trim any trailing blank lines from the accumulated output
			for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
				out = out[:len(out)-1]
			}
			return strings.Join(out, "\n")
		}

		// Also detect an extended Dependabot block that starts with a plain header line
		// e.g. a line containing "dependabot" and "rebase" or other action keywords
		if strings.Contains(low, "dependabot") {
			// Look ahead a few lines to see if this is indeed the commands block
			lookAhead := strings.ToLower(strings.Join(lines[i:minInt(i+8, len(lines))], " \n "))
			if strings.Contains(lookAhead, "@dependabot") || strings.Contains(lookAhead, "rebase") || strings.Contains(lookAhead, "recreate") || strings.Contains(lookAhead, "merge") {
				for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1]
				}
				return strings.Join(out, "\n")
			}
		}

		out = append(out, lines[i])
	}

	// No Dependabot block found; return original input trimmed of trailing whitespace
	return strings.TrimRightFunc(strings.Join(out, "\n"), func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func removeMultipleNewlines(input string) string {
	var builder strings.Builder
	newlineCount := 0

	for _, char := range input {
		if char == '\n' {
			newlineCount++
			if newlineCount <= 2 {
				builder.WriteRune(char)
			}
		} else {
			builder.WriteRune(char)
			newlineCount = 0
		}
	}

	return builder.String()
}
