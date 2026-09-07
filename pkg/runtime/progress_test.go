package runtime

import "testing"

func TestUpdateSdCppProgressSampling(t *testing.T) {
	cur := ModelProgress{}
	cur = updateSdCppProgress(cur, "stable-diffusion.cpp:3361 - generating image: 1/1 - seed 43")
	if cur.Phase != "采样" {
		t.Fatalf("expected sampling phase, got %q", cur.Phase)
	}
	cur = updateSdCppProgress(cur, "  |============>| 3/8 - 2.15s/it")
	if cur.Step != 3 || cur.Total != 8 {
		t.Fatalf("unexpected step: %d/%d", cur.Step, cur.Total)
	}
	if cur.Percent < 40 || cur.Percent > 50 {
		t.Fatalf("percent out of range for step 3/8: %d", cur.Percent)
	}
	cur = updateSdCppProgress(cur, "stable-diffusion.cpp:3403 - sampling completed, taking 17.25s")
	if cur.Percent != 92 {
		t.Fatalf("expected 92 after sampling, got %d", cur.Percent)
	}
	cur = updateSdCppProgress(cur, "stable-diffusion.cpp:3431 - decode_first_stage completed, taking 2.35s")
	if cur.Percent != 95 || cur.Phase != "解码" {
		t.Fatalf("expected decode 95, got %d %q", cur.Percent, cur.Phase)
	}
	cur = updateSdCppProgress(cur, "stable-diffusion.cpp:3741 - generate_image completed in 19.66s")
	if cur.Percent != 100 || cur.Phase != "完成" {
		t.Fatalf("expected done 100, got %d %q", cur.Percent, cur.Phase)
	}
}

func TestUpdateSdCppProgressLoading(t *testing.T) {
	cur := ModelProgress{Phase: "starting", Percent: 2}
	cur = updateSdCppProgress(cur, "model.cpp:1399 - loading tensors from C:\\models\\x.gguf")
	cur = updateSdCppProgress(cur, "  |====>| 140/1095 - 686.27it/s")
	if cur.Phase != "加载模型" || cur.Total != 1095 {
		t.Fatalf("expected loading phase, got %q total=%d", cur.Phase, cur.Total)
	}
	if cur.Percent < 2 || cur.Percent > 14 {
		t.Fatalf("loading percent out of range: %d", cur.Percent)
	}
}

func TestSdProgressPipeSplitsCR(t *testing.T) {
	var got ModelProgress
	pipe := newSdProgressPipe(func(p ModelProgress) { got = p }, nopWriter{})
	_, _ = pipe.Write([]byte("generating image: 1/1\r  |==>| 1/8 - 2.15s/it\r  |=====>| 5/8 - 2.15s/it\r"))
	if got.Step != 5 || got.Total != 8 {
		t.Fatalf("pipe should track last step, got %d/%d", got.Step, got.Total)
	}
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
