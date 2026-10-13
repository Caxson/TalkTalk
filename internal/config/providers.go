package config

// Provider and conversation settings.
const (
	EnvLLMBaseURL   = "TALKTALK_LLM_BASE_URL"
	EnvLLMModel     = "TALKTALK_LLM_MODEL"
	EnvLLMAPIKey    = "TALKTALK_LLM_API_KEY"
	EnvLLMExtraBody = "TALKTALK_LLM_EXTRA_BODY"
	EnvLLMUseProxy  = "TALKTALK_LLM_USE_PROXY"

	EnvDashScopeAPIKey = "DASHSCOPE_API_KEY"
	EnvASRModel        = "TALKTALK_ASR_MODEL"
	EnvASRSilenceMs    = "TALKTALK_ASR_SILENCE_MS"
	EnvTTSModel        = "TALKTALK_TTS_MODEL"
	EnvTTSVoice        = "TALKTALK_TTS_VOICE"
	EnvTTSSampleRate   = "TALKTALK_TTS_SAMPLE_RATE"

	EnvSystemPrompt     = "TALKTALK_SYSTEM_PROMPT"
	EnvInterruptMinRune = "TALKTALK_INTERRUPT_MIN_RUNES"
	EnvMaxSessions      = "TALKTALK_MAX_SESSIONS"
	EnvICEServers       = "TALKTALK_ICE_SERVERS"

	EnvLipsyncModel   = "TALKTALK_LIPSYNC_MODEL"
	EnvOnnxRuntimeLib = "TALKTALK_ONNXRUNTIME_LIB"
	EnvMouthGain      = "TALKTALK_MOUTH_GAIN"
	EnvAssetsDir      = "TALKTALK_ASSETS_DIR"
)

// Defaults chosen from measured latency (see README).
const (
	DefaultLLMBaseURL    = "https://api.deepseek.com"
	DefaultLLMModel      = "deepseek-flash"
	DefaultASRModel      = "paraformer-realtime-v2"
	DefaultASRSilenceMs  = 500
	DefaultTTSModel      = "cosyvoice-v3-flash"
	DefaultTTSVoice      = "longxiaochun_v3"
	DefaultTTSSampleRate = 24000
	DefaultMaxSessions   = 4
	DefaultSystemPrompt  = "你是 TalkTalk，一个用语音和人聊天的伙伴。你说的每个字都会被直接念出来，所以：" +
		"用自然的中文口语，像朋友聊天；每次回答一般一到三句话，除非对方要你详细讲；" +
		"不要用列表、标题、表情符号或任何 Markdown；数字和符号写成读得出来的样子。" +
		"如果对方的话像是没说完或者听不清，就简短地问一句。"
)

// LLM selects an OpenAI-compatible chat model.
type LLM struct {
	BaseURL     string
	Model       string
	APIKey      string
	ExtraBody   map[string]any
	UseEnvProxy bool
}

// Speech configures DashScope recognition and synthesis.
type Speech struct {
	APIKey        string
	ASRModel      string
	ASRSilenceMs  int
	TTSModel      string
	TTSVoice      string
	TTSSampleRate int
}

// Avatar configures lip-sync and the browser avatar. An empty ModelPath
// disables the avatar: the client falls back to the transcript only.
type Avatar struct {
	ModelPath   string
	LibraryPath string
	MouthGain   float64
	// AssetsDir holds downloaded client assets (renderer, avatar), served
	// under /assets/.
	AssetsDir string
}

// Conversation tunes the dialog engine and session limits.
type Conversation struct {
	SystemPrompt      string
	InterruptMinRunes int
	MaxSessions       int
	ICEServers        []string
}

func loadProviders(r *reader, cfg *Config) {
	cfg.LLM = LLM{
		BaseURL: r.str(EnvLLMBaseURL, DefaultLLMBaseURL),
		Model:   r.str(EnvLLMModel, DefaultLLMModel),
		APIKey:  r.required(EnvLLMAPIKey),
		// DeepSeek V4 thinks by default, which costs seconds of latency
		// before the first spoken word.
		ExtraBody:   r.jsonObject(EnvLLMExtraBody),
		UseEnvProxy: r.boolean(EnvLLMUseProxy, false),
	}
	if _, set := r.lookup(EnvLLMExtraBody); !set && cfg.LLM.BaseURL == DefaultLLMBaseURL {
		cfg.LLM.ExtraBody = map[string]any{"thinking": map[string]any{"type": "disabled"}}
	}
	cfg.Speech = Speech{
		APIKey:        r.required(EnvDashScopeAPIKey),
		ASRModel:      r.str(EnvASRModel, DefaultASRModel),
		ASRSilenceMs:  r.intIn(EnvASRSilenceMs, DefaultASRSilenceMs, 200, 6000),
		TTSModel:      r.str(EnvTTSModel, DefaultTTSModel),
		TTSVoice:      r.str(EnvTTSVoice, DefaultTTSVoice),
		TTSSampleRate: r.oneOf(EnvTTSSampleRate, DefaultTTSSampleRate, 8000, 16000, 24000, 48000),
	}
	cfg.Avatar = Avatar{
		ModelPath:   r.str(EnvLipsyncModel, ""),
		LibraryPath: r.str(EnvOnnxRuntimeLib, ""),
		// The model opens the mouth little for Chinese; 1.6 measured on
		// CosyVoice speech gives a natural jaw range.
		MouthGain: r.floatIn(EnvMouthGain, 1.6, 0.5, 4),
		AssetsDir: r.str(EnvAssetsDir, "assets"),
	}
	cfg.Conversation = Conversation{
		SystemPrompt:      r.str(EnvSystemPrompt, DefaultSystemPrompt),
		InterruptMinRunes: r.intIn(EnvInterruptMinRune, 2, 1, 20),
		MaxSessions:       r.intIn(EnvMaxSessions, DefaultMaxSessions, 1, 1000),
		ICEServers:        r.list(EnvICEServers),
	}
}
