package main

import (
	"strings"

	"charm.land/glamour/v2"
)

var glamourRenderer *glamour.TermRenderer

// initMarkdown 初始化（或按宽度重建）glamour 渲染器
func initMarkdown(w int) {
	wrapWidth := max(40, w-4) // 最小 40 列，确保正确渲染
	if glamourRenderer != nil {
		return // 宽度变化不重建，glamour 会根据输入自行处理
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dracula"),
		glamour.WithWordWrap(wrapWidth),
	)
	if err != nil {
		return
	}
	glamourRenderer = r
}

// renderMarkdown 将 Markdown 文本渲染为 ANSI 行数组
func renderMarkdown(text string, width int) []string {
	initMarkdown(width)
	if glamourRenderer == nil {
		return strings.Split(text, "\n")
	}
	out, err := glamourRenderer.Render(text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

// streamingMarkdown 缓存「稳定前缀」的渲染结果，增量渲染尾部。
// 借鉴 crush 的 streaming_markdown.go。
type streamingMarkdown struct {
	width              int
	stablePrefix       string
	stablePrefixRender string
}

func (s *streamingMarkdown) Reset() {
	s.width = 0
	s.stablePrefix = ""
	s.stablePrefixRender = ""
}

// Render 渲染 Markdown，复用缓存的稳定前缀。
// 当 findSafeMarkdownBoundary 返回 -1 时回退到全量渲染。
func (s *streamingMarkdown) Render(content string, width int) []string {
	initMarkdown(width)
	if glamourRenderer == nil {
		return strings.Split(content, "\n")
	}

	// 宽度变化或内容不是前缀扩展 → 全量渲染
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.Reset()
		s.width = width
		out := renderMarkdown(content, width)
		s.tryAdvanceFromEmpty(content, width)
		return out
	}

	// 找安全边界
	boundary := findSafeMarkdownBoundary(content)
	if boundary < 0 {
		return renderMarkdown(content, width)
	}

	// 边界已被缓存覆盖 → 只渲染尾部
	if boundary <= len(s.stablePrefix) {
		trail := content[len(s.stablePrefix):]
		return strings.Split(glueRenders(s.stablePrefixRender, renderTrailing(trail)), "\n")
	}

	// 发现新的安全区块
	newChunk := content[len(s.stablePrefix):boundary]
	s.stablePrefixRender = glueRenders(s.stablePrefixRender, renderTrailing(newChunk))
	s.stablePrefix = content[:boundary]

	trail := content[boundary:]
	if trail == "" {
		return strings.Split(s.stablePrefixRender, "\n")
	}
	return strings.Split(glueRenders(s.stablePrefixRender, renderTrailing(trail)), "\n")
}

func (s *streamingMarkdown) tryAdvanceFromEmpty(content string, width int) {
	boundary := findSafeMarkdownBoundary(content)
	if boundary <= 0 {
		return
	}
	prefix := content[:boundary]
	out, err := glamourRenderer.Render(prefix)
	if err != nil {
		return
	}
	s.stablePrefix = prefix
	s.stablePrefixRender = trimMargins(out)
	s.width = width
}

func renderTrailing(text string) string {
	if text == "" {
		return ""
	}
	out, err := glamourRenderer.Render(text)
	if err != nil {
		return text
	}
	return trimMargins(out)
}

func glueRenders(prefix, trail string) string {
	prefix = trimMargins(prefix)
	trail = trimMargins(trail)
	switch {
	case prefix == "" && trail == "":
		return ""
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

func trimMargins(s string) string {
	return strings.Trim(s, " \t\n")
}

// 以下代码移植自 crush (github.com/charmbracelet/crush/internal/ui/chat/streaming_markdown.go)
// 安全边界检测算法，保证分片 glamour 渲染的正确性。

func findSafeMarkdownBoundary(content string) int {
	if len(content) == 0 {
		return -1
	}
	for p := blankLineBefore(content, len(content)); p > 0; p = blankLineBefore(content, p-1) {
		if !isSafeBoundaryAt(content, p) {
			continue
		}
		return p
	}
	return -1
}

func blankLineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	end := until
	for end > 0 {
		nl := strings.LastIndexByte(content[:end], '\n')
		if nl < 0 {
			return -1
		}
		prev := strings.LastIndexByte(content[:nl], '\n')
		for prev >= 0 {
			gap := content[prev+1 : nl]
			if isBlankOrSpaces(gap) {
				return nl + 1
			}
			break
		}
		end = nl
	}
	return -1
}

func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

func isSafeBoundaryAt(content string, p int) bool {
	prefix := content[:p]

	if countFenceLines(prefix)%2 != 0 {
		return false
	}
	if prefixHasOpenHazard(prefix) {
		return false
	}

	lastLine := lastNonBlankLine(prefix)
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}
	return true
}

func prefixHasOpenHazard(prefix string) bool {
	inFence := false
	for line := range splitLines(prefix) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if isListItemMarker(trimmed) {
			return true
		}
		if isHTMLBlockOpener(line) {
			return true
		}
		if isLinkRefDefinition(line) {
			return true
		}
	}
	return false
}

func countFenceLines(s string) int {
	n := 0
	for line := range splitLines(s) {
		if isFenceLine(line) {
			n++
		}
	}
	return n
}

func isFenceLine(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

func lastNonBlankLine(s string) string {
	last := ""
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

func firstNonBlankLine(s string) string {
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func splitLines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start <= len(s)-1 {
			yield(s[start:])
		}
	}
}

func lineOpensConstruct(line string) bool {
	if len(line) > 0 && line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "    ") {
		return true
	}
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}
	if trimmed[0] == '>' {
		return true
	}
	if isListItemMarker(trimmed) {
		return true
	}
	if strings.ContainsRune(line, '|') {
		return true
	}
	if isSetextUnderlineCandidate(trimmed) {
		return true
	}
	return false
}

func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		return len(line) >= 2 && (line[1] == ' ' || line[1] == '\t')
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 || i >= len(line) {
		return false
	}
	if line[i] != '.' && line[i] != ')' {
		return false
	}
	return i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t')
}

func isSetextUnderlineCandidate(line string) bool {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	return j-i >= 1
}

func isHTMLBlockOpener(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}
	if strings.HasPrefix(rest, "<!--") {
		return true
	}
	if strings.HasPrefix(rest, "<?") {
		return true
	}
	if strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}
	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}
	j := 1
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		return false
	}
	i++
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return i < len(line)
}
