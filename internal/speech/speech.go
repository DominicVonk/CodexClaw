package speech

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
)

type Service struct {
	cfg   config.SpeechConfig
	store media.Store
}

func New(cfg config.SpeechConfig, mediaCfg config.MediaConfig) Service {
	return Service{
		cfg:   cfg,
		store: media.NewStore(mediaCfg.Dir),
	}
}

func (s Service) STTEnabled() bool {
	return s.cfg.STT.Enabled && strings.TrimSpace(s.cfg.STT.Command) != ""
}

func (s Service) TTSEnabled() bool {
	return s.cfg.TTS.Enabled && strings.TrimSpace(s.cfg.TTS.Command) != ""
}

func (s Service) Transcribe(ctx context.Context, attachment media.Attachment) (string, error) {
	if !s.STTEnabled() {
		return "", errors.New("speech-to-text is not configured")
	}
	command := expandCommand(s.cfg.STT.Command, map[string]string{
		"input": attachment.Path,
	})
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", commandError("speech-to-text failed", err, stderr.String())
	}
	transcript := strings.TrimSpace(stdout.String())
	if transcript == "" {
		return "", errors.New("speech-to-text produced no transcript")
	}
	return transcript, nil
}

func (s Service) Synthesize(ctx context.Context, text string) (*media.Attachment, error) {
	if !s.TTSEnabled() {
		return nil, errors.New("text-to-speech is not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text-to-speech needs non-empty text")
	}
	mimeType := s.cfg.TTS.MIME
	if mimeType == "" {
		mimeType = "audio/ogg"
	}
	fileName := s.cfg.TTS.FileName
	if fileName == "" {
		fileName = "reply.ogg"
	}

	if strings.Contains(s.cfg.TTS.Command, "{output}") {
		return s.synthesizeToFile(ctx, text, fileName, mimeType)
	}
	return s.synthesizeToStdout(ctx, text, fileName, mimeType)
}

func (s Service) synthesizeToFile(ctx context.Context, text string, fileName string, mimeType string) (*media.Attachment, error) {
	dir := filepath.Join(s.store.Dir, "speech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, "tts-*"+extensionFor(fileName, mimeType))
	if err != nil {
		return nil, err
	}
	outputPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(outputPath)

	command := expandCommand(s.cfg.TTS.Command, map[string]string{
		"output": outputPath,
		"text":   text,
	})
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, commandError("text-to-speech failed", err, stderr.String())
	}
	if info, err := os.Stat(outputPath); err != nil {
		return nil, fmt.Errorf("text-to-speech did not create output file: %w", err)
	} else if info.Size() == 0 {
		return nil, errors.New("text-to-speech created an empty output file")
	}
	attachment, err := media.AttachmentForPath(outputPath, filepath.Base(outputPath), mimeType)
	if err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (s Service) synthesizeToStdout(ctx context.Context, text string, fileName string, mimeType string) (*media.Attachment, error) {
	command := expandCommand(s.cfg.TTS.Command, map[string]string{
		"text": text,
	})
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, commandError("text-to-speech failed", err, stderr.String())
	}
	if stdout.Len() == 0 {
		return nil, errors.New("text-to-speech produced no audio")
	}
	attachment, err := s.store.SaveBytes("speech", fileName, mimeType, stdout.Bytes())
	if err != nil {
		return nil, err
	}
	return &attachment, nil
}

func expandCommand(template string, values map[string]string) string {
	command := template
	for key, value := range values {
		command = strings.ReplaceAll(command, "{"+key+"}", shellQuote(value))
	}
	return command
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func commandError(prefix string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	if len(stderr) > 1200 {
		stderr = stderr[:1200] + "..."
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, stderr)
}

func extensionFor(fileName string, mimeType string) string {
	if ext := filepath.Ext(fileName); ext != "" {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ".ogg"
}
