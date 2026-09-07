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
	in, err := parseImageRequest(`{"model":"zimage-turbo","prompt":"a cat","width":"768","height":null,"steps":""}`)
	if err != nil {
		t.Fatalf("string numbers: %v", err)
	}
	if in.Width != 768 || in.Height != 0 || in.Steps != 0 {
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
