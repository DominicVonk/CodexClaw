package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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

const logIDTail = 4

func Run(ctx context.Context, cfg config.WhatsAppConfig, mediaCfg config.MediaConfig, rt *router.Router) error {
	mediaStore := media.NewStore(mediaCfg.Dir)
	for {
		client, err := connect(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("whatsapp connect failed: %v; retrying in 15s", err)
			if !sleepContext(ctx, 15*time.Second) {
				return ctx.Err()
			}
			continue
		}

		client.AddEventHandler(func(evt any) {
			msg, ok := evt.(*events.Message)
			if !ok {
				return
			}
			if msg.Info.IsFromMe && msg.Info.DeviceSentMeta == nil {
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
			identity := identityFor(ctx, client, msg.Info)
			log.Printf("whatsapp message received chat=%s sender=%s from_me=%v device_sent=%v text=%v attachments=%d", redactedJID(chat), redactedJID(senderFor(client, msg.Info)), msg.Info.IsFromMe, msg.Info.DeviceSentMeta != nil, strings.TrimSpace(text) != "", len(attachments))
			go func() {
				typingCtx, stopTyping := context.WithCancel(ctx)
				defer stopTyping()
				go sendTypingUntilStopped(typingCtx, client, chat)
				err := rt.HandleMessage(ctx, identity, router.Message{Text: text, Attachments: attachments}, func(ctx context.Context, reply string) error {
					return sendText(ctx, client, chat, reply)
				})
				if err != nil {
					log.Printf("whatsapp route failed: %v", err)
				}
			}()
		})

		<-ctx.Done()
		client.Disconnect()
		return ctx.Err()
	}
}

func sendTypingUntilStopped(ctx context.Context, client *whatsmeow.Client, chat types.JID) {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	sendTyping(ctx, client, chat, types.ChatPresenceComposing)
	defer sendTyping(context.Background(), client, chat, types.ChatPresencePaused)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendTyping(ctx, client, chat, types.ChatPresenceComposing)
		}
	}
}

func sendTyping(ctx context.Context, client *whatsmeow.Client, chat types.JID, state types.ChatPresence) {
	if err := client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
		log.Printf("whatsapp typing indicator failed: %v", err)
	}
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

func PairPhone(ctx context.Context, cfg config.WhatsAppConfig, phone string) error {
	client, err := newClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect()
	if client.Store.ID != nil {
		if err := client.ConnectContext(ctx); err != nil {
			return err
		}
		fmt.Println("WhatsApp is already paired.")
		return nil
	}
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := client.ConnectContext(ctx); err != nil {
		return err
	}
	pairRequested := false
	for evt := range qrChan {
		switch evt.Event {
		case whatsmeow.QRChannelEventCode:
			if pairRequested {
				continue
			}
			pairRequested = true
			code, err := client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
			if err != nil {
				return err
			}
			fmt.Println("WhatsApp pairing code:", code)
			fmt.Println("Open WhatsApp > Linked devices > Link with phone number instead, then enter the code.")
		case "success":
			if !waitForLogin(ctx, client, 30*time.Second) {
				client.Disconnect()
				return errors.New("whatsapp pairing succeeded but login did not complete")
			}
			fmt.Println("WhatsApp pairing succeeded.")
			return nil
		case whatsmeow.QRChannelEventError:
			if evt.Error != nil {
				return evt.Error
			}
			return errors.New("whatsapp pairing failed")
		default:
			return fmt.Errorf("whatsapp pairing ended with event %q", evt.Event)
		}
	}
	return errors.New("whatsapp pairing channel closed before success")
}

func connect(ctx context.Context, cfg config.WhatsAppConfig) (*whatsmeow.Client, error) {
	client, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
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
			case whatsmeow.QRChannelEventCode:
				if cfg.QR {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				}
				fmt.Println("WhatsApp QR code:", evt.Code)
			case "success":
				if !waitForLogin(ctx, client, 30*time.Second) {
					client.Disconnect()
					return nil, errors.New("whatsapp QR pairing succeeded but login did not complete")
				}
				return client, nil
			case whatsmeow.QRChannelEventError:
				client.Disconnect()
				if evt.Error != nil {
					return nil, evt.Error
				}
				return nil, errors.New("whatsapp QR pairing failed")
			default:
				client.Disconnect()
				return nil, fmt.Errorf("whatsapp QR pairing ended with event %q", evt.Event)
			}
		}
		client.Disconnect()
		return nil, errors.New("whatsapp QR pairing channel closed before success")
	}

	if err := client.ConnectContext(ctx); err != nil {
		return nil, err
	}
	if !waitForLogin(ctx, client, 30*time.Second) {
		client.Disconnect()
		return nil, errors.New("whatsapp connected but login did not complete")
	}
	return client, nil
}

func newClient(ctx context.Context, cfg config.WhatsAppConfig) (*whatsmeow.Client, error) {
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
	return client, nil
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func waitForLogin(ctx context.Context, client *whatsmeow.Client, timeout time.Duration) bool {
	if client.IsLoggedIn() {
		return true
	}
	done := make(chan bool, 1)
	go func() {
		done <- client.WaitForConnection(timeout)
	}()
	select {
	case ok := <-done:
		return ok
	case <-ctx.Done():
		return false
	}
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

func identityFor(ctx context.Context, client *whatsmeow.Client, info types.MessageInfo) router.Identity {
	chat := info.Chat
	sender := senderFor(client, info)
	chatID := chat.String()
	senderID := sender.User
	if senderID == "" {
		senderID = sender.String()
	}
	allowKeys := allowKeysFor(ctx, client, sender, info.SenderAlt)
	return router.Identity{
		Source:    "whatsapp",
		ChatID:    chatID,
		SenderID:  senderID,
		SessionID: chatID,
		AllowKeys: allowKeys,
	}
}

func senderFor(client *whatsmeow.Client, info types.MessageInfo) types.JID {
	if !info.Sender.IsEmpty() {
		return info.Sender
	}
	if !info.SenderAlt.IsEmpty() {
		return info.SenderAlt
	}
	if info.IsFromMe && client != nil && client.Store != nil && client.Store.ID != nil {
		return *client.Store.ID
	}
	return info.Chat
}

func allowKeysFor(ctx context.Context, client *whatsmeow.Client, sender types.JID, senderAlt types.JID) []string {
	var keys []string
	addJIDKeys := func(jid types.JID) {
		if jid.IsEmpty() {
			return
		}
		if jid.User != "" {
			keys = append(keys, "whatsapp:"+jid.User)
		}
		keys = append(keys, "whatsapp:"+jid.String())
	}
	addJIDKeys(sender)
	addJIDKeys(senderAlt)
	for _, jid := range []types.JID{sender, senderAlt} {
		if jid.IsEmpty() || client.Store == nil || client.Store.LIDs == nil {
			continue
		}
		if alt, err := client.Store.GetAltJID(ctx, jid); err == nil {
			addJIDKeys(alt)
		}
	}
	return dedupeStrings(keys)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func redactedJID(jid types.JID) string {
	if jid.IsEmpty() {
		return "empty"
	}
	user := jid.User
	if len(user) > logIDTail {
		user = "..." + user[len(user)-logIDTail:]
	}
	if user == "" {
		return jid.Server
	}
	return user + "@" + jid.Server
}
