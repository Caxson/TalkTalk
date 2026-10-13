package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/Caxson/TalkTalk/internal/asr"
	"github.com/Caxson/TalkTalk/internal/cast"
	"github.com/Caxson/TalkTalk/internal/config"
	"github.com/Caxson/TalkTalk/internal/dashscope"
	"github.com/Caxson/TalkTalk/internal/dialog"
	"github.com/Caxson/TalkTalk/internal/lipsync"
	"github.com/Caxson/TalkTalk/internal/llm"
	"github.com/Caxson/TalkTalk/internal/session"
	"github.com/Caxson/TalkTalk/internal/tts"
)

// newSessions builds the provider clients and the session manager.
func newSessions(cfg config.Config, roster *cast.Cast, logger *slog.Logger) (*session.Manager, error) {
	speechConn := dashscope.Config{APIKey: cfg.Speech.APIKey}

	recognizer, err := asr.NewDashScope(asr.DashScopeConfig{
		Conn:                 speechConn,
		Model:                cfg.Speech.ASRModel,
		MaxSentenceSilenceMs: cfg.Speech.ASRSilenceMs,
	})
	if err != nil {
		return nil, err
	}
	chat, err := llm.NewOpenAIClient(llm.OpenAIConfig{
		BaseURL:     cfg.LLM.BaseURL,
		APIKey:      cfg.LLM.APIKey,
		Model:       cfg.LLM.Model,
		ExtraBody:   cfg.LLM.ExtraBody,
		UseEnvProxy: cfg.LLM.UseEnvProxy,
	})
	if err != nil {
		return nil, err
	}
	synthCfg := tts.DashScopeConfig{
		Conn:       speechConn,
		Model:      cfg.Speech.TTSModel,
		Voice:      cfg.Speech.TTSVoice,
		SampleRate: cfg.Speech.TTSSampleRate,
	}
	persona := personaOf(roster, cfg.Conversation.SystemPrompt, cfg.Speech.TTSVoice)
	for _, ch := range roster.List() {
		p, _ := persona(ch.ID)
		voiced := synthCfg
		voiced.Voice = p.Voice
		if _, err := tts.NewDashScope(voiced); err != nil {
			return nil, fmt.Errorf("character %s: %w", ch.ID, err) // validate once at startup, not per session
		}
	}

	lips, err := newLips(cfg, logger)
	if err != nil {
		return nil, err
	}

	return session.NewManager(session.Deps{
		Lips:       lips,
		Recognizer: recognizer,
		LLM:        chat,
		NewSynth: func(voice string) (session.Synth, error) {
			voiced := synthCfg
			voiced.Voice = voice
			return tts.NewDashScope(voiced)
		},
		Persona: persona,
		Dialog: dialog.Config{
			SystemPrompt:      cfg.Conversation.SystemPrompt,
			Affect:            true,
			InterruptMinRunes: cfg.Conversation.InterruptMinRunes,
		},
		ICEServers: cfg.Conversation.ICEServers,
		Max:        cfg.Conversation.MaxSessions,
		Logger:     logger,
	})
}

// newLips loads the lip-sync model when configured. A configured model that
// fails to load is an error: a silently missing avatar is worse.
func newLips(cfg config.Config, logger *slog.Logger) (func(int) (dialog.LipStream, error), error) {
	if cfg.Avatar.ModelPath == "" {
		logger.Info("avatar disabled: TALKTALK_LIPSYNC_MODEL not set (run make assets)")
		return nil, nil
	}
	start := time.Now()
	model, err := lipsync.Load(lipsync.Config{
		ModelPath:   cfg.Avatar.ModelPath,
		LibraryPath: cfg.Avatar.LibraryPath,
		MouthGain:   cfg.Avatar.MouthGain,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("lip-sync model loaded", "path", cfg.Avatar.ModelPath, "took", time.Since(start))
	return func(rate int) (dialog.LipStream, error) { return model.NewStream(rate) }, nil
}
