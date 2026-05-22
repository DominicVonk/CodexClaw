package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
	"github.com/DominicVonk/CodexClaw/internal/router"
)

func Run(ctx context.Context, cfg config.TelegramConfig, mediaCfg config.MediaConfig, rt *router.Router) error {
	if cfg.Token == "" {
		return errors.New("telegram token is empty")
	}

	b, err := bot.New(
		cfg.Token,
		bot.WithHTTPClient(cfg.Timeout(), &http.Client{Timeout: cfg.Timeout() + cfg.Timeout()/2}),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message"}),
		bot.WithDefaultHandler(handleUpdate(media.NewStore(mediaCfg.Dir), rt)),
		bot.WithErrorsHandler(func(err error) {
			log.Printf("telegram bot failed: %v", err)
		}),
	)
	if err != nil {
		return err
	}

	if err := setCommands(ctx, b); err != nil {
		return fmt.Errorf("set telegram commands: %w", err)
	}

	b.Start(ctx)
	return ctx.Err()
}

func setCommands(ctx context.Context, b *bot.Bot) error {
	_, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: telegramCommands(),
	})
	return err
}

func telegramCommands() []models.BotCommand {
	return []models.BotCommand{
		{Command: "new", Description: "Start a fresh Codex session"},
		{Command: "session", Description: "List or switch saved sessions"},
		{Command: "status", Description: "Show model, reasoning, tokens, and compaction"},
		{Command: "model", Description: "Show or switch the active model"},
		{Command: "reasoning", Description: "Show or switch reasoning level"},
		{Command: "skills", Description: "List built-in and Codex skills"},
		{Command: "remember", Description: "Save a persistent memory"},
		{Command: "memory", Description: "List saved memories"},
		{Command: "forget", Description: "Delete saved memories"},
	}
}

func handleUpdate(mediaStore media.Store, rt *router.Router) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}

		msg := update.Message
		text := messageText(msg)
		attachments, err := downloadAttachments(ctx, b, mediaStore, msg)
		if err != nil {
			log.Printf("telegram attachment download failed: %v", err)
		}
		if text == "" && len(attachments) == 0 {
			return
		}
		chatID := msg.Chat.ID
		messageThreadID := msg.MessageThreadID
		identity := identityFor(chatID, senderID(msg), messageThreadID)

		go func() {
			err := rt.HandleMessage(ctx, identity, router.Message{Text: text, Attachments: attachments}, func(ctx context.Context, text string) error {
				_, err := b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID:          chatID,
					MessageThreadID: messageThreadID,
					Text:            text,
				})
				return err
			})
			if err != nil {
				log.Printf("telegram route failed: %v", err)
			}
		}()
	}
}

func messageText(msg *models.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

func downloadAttachments(ctx context.Context, b *bot.Bot, mediaStore media.Store, msg *models.Message) ([]media.Attachment, error) {
	var attachments []media.Attachment
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		attachment, err := downloadTelegramFile(ctx, b, mediaStore, "telegram", photo.FileID, photo.FileUniqueID+".jpg", "image/jpeg")
		if err != nil {
			return attachments, err
		}
		attachments = append(attachments, attachment)
	}
	if msg.Document != nil {
		name := msg.Document.FileName
		if name == "" {
			name = msg.Document.FileUniqueID
		}
		attachment, err := downloadTelegramFile(ctx, b, mediaStore, "telegram", msg.Document.FileID, name, msg.Document.MimeType)
		if err != nil {
			return attachments, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func downloadTelegramFile(ctx context.Context, b *bot.Bot, mediaStore media.Store, source string, fileID string, name string, mimeType string) (media.Attachment, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return media.Attachment{}, err
	}
	if name == "" {
		name = filepath.Base(file.FilePath)
	}
	return mediaStore.Download(ctx, source, name, mimeType, b.FileDownloadLink(file))
}

func identityFor(chatID int64, senderID int64, messageThreadID int) router.Identity {
	chat := formatID(chatID)
	sender := formatID(senderID)
	identity := router.Identity{
		Source:    "telegram",
		ChatID:    chat,
		SenderID:  sender,
		SessionID: chat,
	}
	if messageThreadID != 0 {
		identity.SessionID = fmt.Sprintf("%s:%d", chat, messageThreadID)
	}
	return identity
}

func senderID(msg *models.Message) int64 {
	if msg.From == nil {
		return 0
	}
	return msg.From.ID
}

func formatID(id int64) string {
	return fmt.Sprintf("%d", id)
}
