package config

import (
	"strings"
	"testing"
)

func TestProviderDefaults(t *testing.T) {
	cfg, err := Load(lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.BaseURL != DefaultLLMBaseURL || cfg.LLM.Model != DefaultLLMModel || cfg.LLM.APIKey != "llm-key" {
		t.Errorf("LLM = %+v", cfg.LLM)
	}
	th, _ := cfg.LLM.ExtraBody["thinking"].(map[string]any)
	if th["type"] != "disabled" {
		t.Errorf("default DeepSeek should disable thinking, got %v", cfg.LLM.ExtraBody)
	}
	if cfg.LLM.UseEnvProxy {
		t.Error("domestic LLM must not use the env proxy by default")
	}
	s := cfg.Speech
	if s.APIKey != "ds-key" || s.ASRModel != DefaultASRModel || s.ASRSilenceMs != 500 ||
		s.TTSModel != DefaultTTSModel || s.TTSVoice != DefaultTTSVoice || s.TTSSampleRate != 24000 {
		t.Errorf("Speech = %+v", s)
	}
	c := cfg.Conversation
	if c.SystemPrompt == "" || c.InterruptMinRunes != 2 || c.MaxSessions != 4 || len(c.ICEServers) != 0 {
		t.Errorf("Conversation = %+v", c)
	}
}

func TestProviderOverrides(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{
		EnvLLMBaseURL:    "https://ark.cn-beijing.volces.com/api/v3",
		EnvLLMModel:      "doubao-seed-2-0-mini-260215",
		EnvLLMExtraBody:  `{"thinking":{"type":"disabled"},"temperature":0.7}`,
		EnvLLMUseProxy:   "true",
		EnvTTSSampleRate: "48000",
		EnvASRSilenceMs:  "800",
		EnvMaxSessions:   "1",
		EnvICEServers:    "stun:a:3478, stun:b:3478,",
		EnvSystemPrompt:  "你是测试助手",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.ExtraBody["temperature"] != 0.7 || !cfg.LLM.UseEnvProxy {
		t.Errorf("LLM = %+v", cfg.LLM)
	}
	if cfg.Speech.TTSSampleRate != 48000 || cfg.Speech.ASRSilenceMs != 800 {
		t.Errorf("Speech = %+v", cfg.Speech)
	}
	if got := cfg.Conversation.ICEServers; len(got) != 2 || got[1] != "stun:b:3478" {
		t.Errorf("ICEServers = %q", got)
	}
	if cfg.Conversation.SystemPrompt != "你是测试助手" || cfg.Conversation.MaxSessions != 1 {
		t.Errorf("Conversation = %+v", cfg.Conversation)
	}
}

func TestNonDefaultLLMGetsNoImplicitExtraBody(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{EnvLLMBaseURL: "https://other.example/v1"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.ExtraBody != nil {
		t.Errorf("ExtraBody = %v, want nil for non-DeepSeek endpoints", cfg.LLM.ExtraBody)
	}
}

func TestProviderValidationReportsEverything(t *testing.T) {
	_, err := Load(lookupFrom(map[string]string{
		EnvLLMAPIKey:       "-",
		EnvDashScopeAPIKey: "-",
		EnvLLMExtraBody:    "[1,2]",
		EnvTTSSampleRate:   "44100",
		EnvASRSilenceMs:    "50",
		EnvMaxSessions:     "zero",
		EnvLLMUseProxy:     "maybe",
	}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{EnvLLMAPIKey, EnvDashScopeAPIKey, EnvLLMExtraBody, EnvTTSSampleRate, EnvASRSilenceMs, EnvMaxSessions, EnvLLMUseProxy} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s:\n%v", key, err)
		}
	}
	if strings.Contains(err.Error(), "llm-key") || strings.Contains(err.Error(), "ds-key") {
		t.Error("error leaks secret values")
	}
}

func TestAvatarConfig(t *testing.T) {
	cfg, err := Load(lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Avatar.ModelPath != "" || cfg.Avatar.MouthGain != 1.6 || cfg.Avatar.AssetsDir != "assets" {
		t.Errorf("Avatar defaults = %+v", cfg.Avatar)
	}
	cfg, err = Load(lookupFrom(map[string]string{EnvLipsyncModel: "/m.onnx", EnvMouthGain: "2.5", EnvOnnxRuntimeLib: "/lib.so"}))
	if err != nil || cfg.Avatar.ModelPath != "/m.onnx" || cfg.Avatar.MouthGain != 2.5 || cfg.Avatar.LibraryPath != "/lib.so" {
		t.Errorf("Avatar = %+v, err %v", cfg.Avatar, err)
	}
	if _, err := Load(lookupFrom(map[string]string{EnvMouthGain: "10"})); err == nil || !strings.Contains(err.Error(), EnvMouthGain) {
		t.Errorf("out-of-range gain accepted: %v", err)
	}
}
