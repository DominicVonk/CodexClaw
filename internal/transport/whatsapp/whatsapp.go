package whatsapp

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
	"github.com/DominicVonk/CodexClaw/internal/router"
)

func Run(ctx context.Context, cfg config.WhatsAppConfig, mediaCfg config.MediaConfig, rt *router.Router) error {
	client, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect()

	mediaStore := media.NewStore(mediaCfg.Dir)

	client.AddEventHandler(func(evt any) {
		msg, ok := evt.(*events.Message)
		if !ok || msg.Info.IsFromMe {
			return
		}
		text := extractText(msg)
		attachments, err := downloadAttachments(ctx, client, mediaStore, msg)
		if err != nil {
			log.Printf("whatsapp attachment download failed: %v", err)
		}
		if strings.TrimSpace(text) == "" && len(attachments) == 0 {
			return
		}
		chat := msg.Info.Chat
		identity := identityFor(chat, msg.Info.Sender)
		go func() {
			err := rt.HandleMessage(ctx, identity, router.Message{Text: text, Attachments: attachments}, func(ctx context.Context, reply string) error {
				return sendText(ctx, client, chat, reply)
			})
			if err != nil {
				log.Printf("whatsapp route failed: %v", err)
			}
		}()
	})

	<-ctx.Done()
	return ctx.Err()
}

func Login(ctx context.Context, cfg config.WhatsAppConfig) error {
	client, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect()
	fmt.Println("WhatsApp login is ready. Press Ctrl+C to exit.")
	<-ctx.Done()
	return ctx.Err()
}

func connect(ctx context.Context, cfg config.WhatsAppConfig) (*whatsmeow.Client, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.SQLitePath), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + cfg.SQLitePath + "?_pragma=foreign_keys(1)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, nil)
	if err != nil {
		return nil, err
	}
	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	client := whatsmeow.NewClient(deviceStore, nil)

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			return nil, err
		}
		if err := client.ConnectContext(ctx); err != nil {
			return nil, err
		}
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				if cfg.QR {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				}
				fmt.Println("WhatsApp QR code:", evt.Code)
			case "success":
				return client, nil
			default:
				fmt.Println("WhatsApp login event:", evt.Event)
			}
		}
		return client, nil
	}

	if err := client.ConnectContext(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func extractText(msg *events.Message) string {
	if msg.Message == nil {
		return ""
	}
	if text := msg.Message.GetConversation(); text != "" {
		return text
	}
	if image := msg.Message.GetImageMessage(); image != nil {
		return image.GetCaption()
	}
	if doc := msg.Message.GetDocumentMessage(); doc != nil {
		return doc.GetCaption()
	}
	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

func downloadAttachments(ctx context.Context, client *whatsmeow.Client, mediaStore media.Store, msg *events.Message) ([]media.Attachment, error) {
	if msg.Message == nil {
		return nil, nil
	}
	var attachments []media.Attachment
	if image := msg.Message.GetImageMessage(); image != nil {
		data, err := client.Download(ctx, image)
		if err != nil {
			return attachments, err
		}
		name := string(msg.Info.ID) + ".jpg"
		attachment, err := mediaStore.SaveBytes("whatsapp", name, image.GetMimetype(), data)
		if err != nil {
			return attachments, err
		}
		attachments = append(attachments, attachment)
	}
	if doc := msg.Message.GetDocumentMessage(); doc != nil {
		data, err := client.Download(ctx, doc)
		if err != nil {
			return attachments, err
		}
		name := doc.GetFileName()
		if name == "" {
			name = doc.GetTitle()
		}
		if name == "" {
			name = string(msg.Info.ID)
		}
		attachment, err := mediaStore.SaveBytes("whatsapp", name, doc.GetMimetype(), data)
		if err != nil {
			return attachments, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func sendText(ctx context.Context, client *whatsmeow.Client, chat types.JID, text string) error {
	_, err := client.SendMessage(ctx, chat, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

func identityFor(chat types.JID, sender types.JID) router.Identity {
	chatID := chat.String()
	senderID := sender.User
	if senderID == "" {
		senderID = sender.String()
	}
	return router.Identity{
		Source:    "whatsapp",
		ChatID:    chatID,
		SenderID:  senderID,
		SessionID: chatID,
		AllowKeys: []string{
			"whatsapp:" + senderID,
			"whatsapp:" + sender.String(),
		},
	}
}
