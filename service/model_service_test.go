package service

import "testing"

func TestParseImageRequestNumbers(t *testing.T) {
	in, err := parseImageRequest(`{"model":"zimage-turbo","prompt":"a cat","width":1024,"height":768,"steps":9}`)
	if err != nil {
		t.Fatalf("strict json: %v", err)
	}
	if in.Width != 1024 || in.Height != 768 || in.Steps != 9 {
		t.Fatalf("unexpected values: %+v", in)
	}
}

func TestParseImageRequestStringNumbers(t *testing.T) {
	// antd 输入框清空/输入时数字可能以字符串或 null 进入。
	in, err := parseImageRequest(`{"model":"zimage-turbo","prompt":"a cat","width":"768","height":null,"steps":"","seed":"12345"}`)
	if err != nil {
		t.Fatalf("string numbers: %v", err)
	}
	if in.Width != 768 || in.Height != 0 || in.Steps != 0 || in.Seed != 12345 {
		t.Fatalf("unexpected values: %+v", in)
	}
}

func TestParseImageRequestRejectsBad(t *testing.T) {
	if _, err := parseImageRequest(`{"model":"m","prompt":"p","width":"abc"}`); err == nil {
		t.Fatal("expected error for non numeric width")
	}
	if _, err := parseImageRequest(`{"model":123}`); err == nil {
		t.Fatal("expected error for wrong model type")
	}
	if _, err := parseImageRequest(""); err == nil {
		t.Fatal("expected error for empty request")
	}
	if _, err := parseImageRequest(`{bad json`); err == nil {
		t.Fatal("expected error for broken json")
	}
}

func TestParseAudioRequestSpeed(t *testing.T) {
	in, err := parseAudioRequest(`{"model":"qwen3-tts","text":"hi","speed":"1.5"}`)
	if err != nil {
		t.Fatalf("string speed: %v", err)
	}
	if in.Speed != 1.5 {
		t.Fatalf("unexpected speed: %v", in.Speed)
	}
	in2, err := parseAudioRequest(`{"model":"qwen3-tts","text":"hi","speed":null}`)
	if err != nil {
		t.Fatalf("null speed: %v", err)
	}
	if in2.Speed != 0 {
		t.Fatalf("unexpected speed: %v", in2.Speed)
	}
}

func TestBuildImageRequestBodyTurboDefaults(t *testing.T) {
	// zimage-turbo 合并参数：cfg_scale=1.0、default_steps=8（sd.cpp 官方 turbo 值）。
	params := map[string]any{
		"cfg_scale":      1.0,
		"default_width":  float64(1024),
		"default_height": float64(1024),
		"default_steps":  float64(8),
	}
	body := buildImageRequestBody(ImageRequest{Model: "zimage-turbo", Prompt: "a cat"}, params)
	if body["cfg_scale"] != 1.0 {
		t.Fatalf("cfg_scale missing or wrong: %#v", body["cfg_scale"])
	}
	if body["steps"] != 8 {
		t.Fatalf("expected default steps 8, got %#v", body["steps"])
	}
	if body["width"] != 1024 || body["height"] != 1024 {
		t.Fatalf("unexpected size: %#v %#v", body["width"], body["height"])
	}
	if body["seed"] != int64(-1) || body["batch_size"] != 1 {
		t.Fatalf("unexpected seed/batch: %#v", body)
	}
	if np, ok := body["negative_prompt"].(string); !ok || np != "" {
		t.Fatalf("unexpected negative_prompt: %#v", body["negative_prompt"])
	}
}

func TestBuildImageRequestBodyExplicitStepsAndNoCfg(t *testing.T) {
	// 无 cfg_scale 参数时不下发该字段，交由 sd-server 默认；用户显式 steps 优先。
	body := buildImageRequestBody(ImageRequest{Model: "m", Prompt: "p", Width: 512, Height: 512, Steps: 20}, nil)
	if _, ok := body["cfg_scale"]; ok {
		t.Fatalf("cfg_scale should be omitted without manifest value: %#v", body)
	}
	if body["steps"] != 20 {
		t.Fatalf("explicit steps should win, got %#v", body["steps"])
	}
	if body["width"] != 512 {
		t.Fatalf("unexpected width: %#v", body["width"])
	}
	if body["seed"] != int64(-1) {
		t.Fatalf("random seed expected by default, got %#v", body["seed"])
	}
}

func TestBuildImageRequestBodyFixedSeed(t *testing.T) {
	body := buildImageRequestBody(ImageRequest{Model: "m", Prompt: "p", Steps: 8, Seed: 20260907}, nil)
	if body["seed"] != int64(20260907) {
		t.Fatalf("fixed seed expected, got %#v", body["seed"])
	}
}

func TestParamNumber(t *testing.T) {
	if v, ok := paramNumber(map[string]any{"a": "1.5"}, "a"); !ok || v != 1.5 {
		t.Fatalf("string number: %v %v", v, ok)
	}
	if v, ok := paramNumber(map[string]any{"a": 9}, "a"); !ok || v != 9 {
		t.Fatalf("int: %v %v", v, ok)
	}
	if paramInt(map[string]any{"a": 8.0}, "a", 9) != 8 {
		t.Fatal("paramInt should read 8")
	}
	if paramInt(map[string]any{"a": 0.5}, "a", 9) != 9 {
		t.Fatal("non-integer should fall back")
	}
}
