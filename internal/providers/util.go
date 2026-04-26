package providers

import "strings"

func joinPromptLines(lines []string) string {
	return strings.Join(lines, "\n")
}
