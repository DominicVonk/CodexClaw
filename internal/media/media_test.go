package media

import "testing"

func TestKindForAudio(t *testing.T) {
	for _, mimeType := range []string{"audio/ogg", "audio/mpeg", "application/ogg"} {
		if got := KindFor(mimeType); got != "audio" {
			t.Fatalf("expected %s to be audio, got %s", mimeType, got)
		}
	}
}
