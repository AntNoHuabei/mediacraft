package runtime

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// sdProgressToken 匹配 sd-server 日志里的数值进度：
//   - 采样：|======>| 3/8 - 2.15s/it
//   - 加载：|======>| 140/1095 - 686.27it/s
var sdProgressToken = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*-\s*[\d.]+\s*(it/s|s/it)`)

// sdProgressPipe 把 sd-server stdout 切成 (CR/LF) 段，逐段喂给
// updateSdCppProgress，同时原样转发给日志环（sink）。
type sdProgressPipe struct {
	sink   io.Writer
	buf    []byte
	cur    ModelProgress
	notify func(ModelProgress)
}

func newSdProgressPipe(notify func(ModelProgress), sink io.Writer) io.Writer {
	if sink == nil {
		sink = io.Discard
	}
	return &sdProgressPipe{
		sink:   sink,
		notify: notify,
		cur:    ModelProgress{Phase: "starting", Percent: 2, Message: "启动服务"},
	}
}

func indexCRLF(b []byte) int {
	for i, c := range b {
		if c == '\r' || c == '\n' {
			return i
		}
	}
	return -1
}

func (p *sdProgressPipe) Write(b []byte) (int, error) {
	n, err := p.sink.Write(b)
	p.buf = append(p.buf, b...)
	for {
		idx := indexCRLF(p.buf)
		if idx < 0 {
			break
		}
		seg := string(p.buf[:idx])
		p.buf = p.buf[idx+1:]
		next := updateSdCppProgress(p.cur, seg)
		if p.notify != nil && next != p.cur {
			p.notify(next)
		}
		p.cur = next
	}
	return n, err
}

// updateSdCppProgress 纯函数：根据一段 sd-server 日志更新进度快照。
func updateSdCppProgress(cur ModelProgress, seg string) ModelProgress {
	s := strings.TrimSpace(seg)
	if s == "" {
		return cur
	}

	// —— 阶段标记 ——
	switch {
	case strings.Contains(s, "generate_image completed") || strings.Contains(s, "generated image"):
		cur.Phase = "完成"
		cur.Percent = 100
		cur.Message = "生成完成"
	case strings.Contains(s, "sampling completed"):
		if cur.Phase == "采样" {
			cur.Percent = 92
			cur.Message = "采样完成"
		}
	case strings.Contains(s, "decode_first_stage completed") || strings.Contains(s, "decoding 1 latents"):
		cur.Phase = "解码"
		cur.Percent = 95
		cur.Message = "解码画面"
	case strings.Contains(s, "generating image:"):
		cur.Phase = "采样"
		cur.Percent = 18
		cur.Step = 0
		cur.Message = "开始采样"
	case strings.Contains(s, "computing condition"):
		cur.Phase = "提示词编码"
		if cur.Percent < 16 {
			cur.Percent = 16
		}
		cur.Message = "编码提示词"
	case strings.Contains(s, "finished loaded file"):
		cur.Phase = "模型就绪"
		if cur.Percent < 14 {
			cur.Percent = 14
		}
		cur.Message = "模型加载完成"
	case strings.Contains(s, "loading diffusion model") || strings.Contains(s, "loading llm") ||
		strings.Contains(s, "loading vae") || strings.Contains(s, "loading tensors") || strings.Contains(s, "init from"):
		if cur.Phase == "" || cur.Phase == "starting" {
			cur.Phase = "加载模型"
			cur.Percent = 4
			cur.Message = "加载模型"
		}
	}

	if cur.Phase == "完成" {
		return cur
	}

	// —— 数值进度（取最后一段） ——
	matches := sdProgressToken.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return cur
	}
	last := matches[len(matches)-1]
	step, _ := strconv.Atoi(last[1])
	total, _ := strconv.Atoi(last[2])
	if total <= 0 {
		return cur
	}
	switch last[3] {
	case "s/it":
		// 采样步进：每步日志都会带 - xx s/it
		if total <= 512 {
			cur.Phase = "采样"
			cur.Step = step
			cur.Total = total
			cur.Percent = 18 + step*74/total
			if cur.Percent > 91 {
				cur.Percent = 91
			}
			cur.Message = fmt.Sprintf("采样 %d/%d", step, total)
		}
	case "it/s":
		// 加载权重（张量计数）阶段
		if cur.Phase == "" || cur.Phase == "starting" || cur.Phase == "加载模型" {
			cur.Phase = "加载模型"
			cur.Step = step
			cur.Total = total
			cur.Percent = 2 + step*12/total
			if cur.Percent > 14 {
				cur.Percent = 14
			}
			cur.Message = fmt.Sprintf("加载权重 %d/%d", step, total)
		}
	}
	return cur
}
