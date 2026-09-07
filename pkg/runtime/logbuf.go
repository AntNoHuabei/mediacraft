package runtime

import (
	"regexp"
	"strings"
	"sync"
)

const (
	// defaultRecentLogLines 环形日志最大行数。
	defaultRecentLogLines = 512
	// recentLogTailBytes 错误详情中附带日志尾部的最大字节数。
	recentLogTailBytes = 8000
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// recentLog 子进程 stdout/stderr 的按行环形缓冲：既作为调试日志，
// 也在启动失败/健康失败时回填最近日志作为错误详情。
type recentLog struct {
	mu      sync.Mutex
	lines   []string
	partial strings.Builder
	max     int
}

func newRecentLog(max int) *recentLog {
	if max <= 0 {
		max = defaultRecentLogLines
	}
	return &recentLog{max: max}
}

// Write 按 \r / \n 切分写入日志行，并剥离 ANSI 转义。
func (b *recentLog) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\r' {
			// 常见进度输出用 \r 原地刷新：遇到 \r 先把已有内容当一行，
			// 再丢弃本行，避免环形缓冲被刷屏（最新内容在后续换行行里）。
			b.push(p[start:i])
			b.partial.Reset()
			start = i + 1
			continue
		}
		if c == '\n' {
			b.push(p[start:i])
			b.partial.Reset()
			start = i + 1
		}
	}
	if start < len(p) {
		b.partial.Write(p[start:])
	}
	return len(p), nil
}

func (b *recentLog) push(segment []byte) {
	line := ansiEscape.ReplaceAllString(strings.TrimRight(string(segment), " \t"), "")
	if line == "" {
		return
	}
	if len(b.lines) >= b.max {
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

// SnapshotLines 返回全部日志行（旧→新）。
func (b *recentLog) SnapshotLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Text 返回最近日志文本（旧→新，含尾部未换行片段）。
func (b *recentLog) Text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb := strings.Builder{}
	for _, line := range b.lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(b.partial.String())
	return sb.String()
}

// Tail 返回日志尾部至多 n 字节文本。
func (b *recentLog) Tail(n int) string {
	text := b.Text()
	if n <= 0 || len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

func (b *recentLog) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
	b.partial.Reset()
}
