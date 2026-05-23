package speech

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
)

func TestTranscribeUsesCommandStdout(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(input, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(config.SpeechConfig{
		TimeoutSeconds: 5,
		STT:            config.SpeechSTTConfig{Enabled: true, Command: "printf 'hello from %s' {input}"},
	}, config.MediaConfig{Dir: dir})
	got, err := svc.Transcribe(context.Background(), media.Attachment{Kind: "audio", Path: input, Name: "voice.ogg", MIME: "audio/ogg"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hello from "+input) {
		t.Fatalf("unexpected transcript: %q", got)
	}
}

func TestSynthesizeCanWriteOutputFile(t *testing.T) {
	dir := t.TempDir()
	svc := New(config.SpeechConfig{
		TimeoutSeconds: 5,
		TTS: config.SpeechTTSConfig{
			Enabled:  true,
			Command:  "printf 'audio:%s' {text} > {output}",
			MIME:     "audio/ogg",
			FileName: "reply.ogg",
		},
	}, config.MediaConfig{Dir: dir})
	attachment, err := svc.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Kind != "audio" || attachment.MIME != "audio/ogg" {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
	data, err := os.ReadFile(attachment.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "audio:hello" {
		t.Fatalf("unexpected audio bytes: %q", string(data))
	}
}
