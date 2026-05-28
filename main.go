package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"

	"rosseti-bot/rosseti"
)

const (
	maxMsgLen  = 4096
	targetRegion = "19"
	targetRaion  = "Алтайский р-н"
	targetGorod  = "д Кайбалы"
)

var (
	recordsCache struct {
		mu      sync.RWMutex
		records []rosseti.ShutdownRecord
	}
	db *sql.DB
)

func initDB() error {
	var err error
	db, err = sql.Open("sqlite", "data.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS subscriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			street TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(chat_id, street)
		);
		CREATE TABLE IF NOT EXISTS sent_notifications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			outage_id TEXT NOT NULL,
			street TEXT NOT NULL,
			sent_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(chat_id, outage_id)
		);
	`)
	if err != nil {
		return fmt.Errorf("init tables: %w", err)
	}
	return nil
}

func addSubscription(chatID int64, street string) error {
	_, err := db.Exec("INSERT OR IGNORE INTO subscriptions (chat_id, street) VALUES (?, ?)", chatID, street)
	return err
}

func removeSubscription(chatID int64, street string) error {
	_, err := db.Exec("DELETE FROM subscriptions WHERE chat_id = ? AND street = ?", chatID, street)
	return err
}

func getSubscriptions(chatID int64) ([]string, error) {
	rows, err := db.Query("SELECT street FROM subscriptions WHERE chat_id = ? ORDER BY street", chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var streets []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		streets = append(streets, s)
	}
	return streets, nil
}

func getAllSubscriptions() (map[int64][]string, error) {
	rows, err := db.Query("SELECT chat_id, street FROM subscriptions ORDER BY chat_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subs := make(map[int64][]string)
	for rows.Next() {
		var chatID int64
		var street string
		if err := rows.Scan(&chatID, &street); err != nil {
			return nil, err
		}
		subs[chatID] = append(subs[chatID], street)
	}
	return subs, nil
}

func isSent(chatID int64, outageID string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sent_notifications WHERE chat_id = ? AND outage_id = ?", chatID, outageID).Scan(&count)
	return count > 0, err
}

func markSent(chatID int64, outageID, street string) error {
	_, err := db.Exec("INSERT OR IGNORE INTO sent_notifications (chat_id, outage_id, street) VALUES (?, ?, ?)", chatID, outageID, street)
	return err
}

func sendLong(bot *tgbotapi.BotAPI, chatID int64, text string) {
	for len(text) > 0 {
		chunk := text
		if len(chunk) > maxMsgLen {
			chunk = chunk[:maxMsgLen]
			if idx := strings.LastIndex(chunk, "\n\n"); idx > len(chunk)/2 {
				chunk = chunk[:idx]
			}
		}
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.DisableWebPagePreview = true
		bot.Send(msg)
		if len(chunk) >= len(text) {
			break
		}
		text = text[len(chunk):]
	}
}

func notifyOutage(bot *tgbotapi.BotAPI, chatID int64, r rosseti.ShutdownRecord) {
	msg := fmt.Sprintf(`⚠️ <b>ОТКЛЮЧЕНИЕ ЭЛЕКТРОЭНЕРГИИ</b>

📍 <b>%s</b>
📅 %s — %s
⏰ %s — %s
🔧 %s`,
		r.Street,
		r.DateStart, r.DateFinish,
		r.TimeStart, r.TimeFinish,
		strings.TrimSpace(r.Res))

	tgMsg := tgbotapi.NewMessage(chatID, msg)
	tgMsg.ParseMode = "HTML"
	tgMsg.DisableWebPagePreview = true
	bot.Send(tgMsg)
}

func processOutages(bot *tgbotapi.BotAPI, records []rosseti.ShutdownRecord) {
	subs, err := getAllSubscriptions()
	if err != nil {
		log.Printf("get subscriptions: %v", err)
		return
	}

	for chatID, streets := range subs {
		streetLower := make(map[string]bool)
		for _, s := range streets {
			streetLower[strings.ToLower(s)] = true
		}

		for _, o := range records {
			if !streetLower[strings.ToLower(o.Street)] {
				continue
			}
			sent, err := isSent(chatID, o.ID)
			if err != nil {
				log.Printf("check sent: %v", err)
				continue
			}
			if sent {
				continue
			}
			log.Printf("Notifying user %d about outage %s on %s", chatID, o.ID, o.Street)
			notifyOutage(bot, chatID, o)
			if err := markSent(chatID, o.ID, o.Street); err != nil {
				log.Printf("mark sent: %v", err)
			}
		}
	}
}

func filterKaybaly(records []rosseti.ShutdownRecord) []rosseti.ShutdownRecord {
	raionLower := strings.ToLower(targetRaion)
	gorodLower := strings.ToLower(targetGorod)
	var result []rosseti.ShutdownRecord
	for _, r := range records {
		if r.Region == targetRegion &&
			strings.Contains(strings.ToLower(r.Raion), raionLower) &&
			strings.Contains(strings.ToLower(r.Gorod), gorodLower) {
			result = append(result, r)
		}
	}
	return result
}

func handleOutagesWebhook(bot *tgbotapi.BotAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apiKey := os.Getenv("PARSER_API_KEY")
		if apiKey != "" && r.Header.Get("X-API-Key") != apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var payload rosseti.OutagesPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		if len(payload.Outages) == 0 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"ok":true,"processed":0}`))
			return
		}

		recordsCache.mu.Lock()
		recordsCache.records = payload.Outages
		recordsCache.mu.Unlock()

		log.Printf("Received %d outage records from parser", len(payload.Outages))

		go processOutages(bot, payload.Outages)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"ok":true,"processed":%d}`, len(payload.Outages))))
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(`{"ok":true}`))
}

func handleStart(bot *tgbotapi.BotAPI, chatID int64) {
	text := `🔌 Бот отключений — д Кайбалы (Россети Сибирь)

Команды:
/add <улица> — подписаться
	Пример: /add ул Ленина
/remove <улица> — отписаться
/list — мои улицы
/check — проверить отключения сейчас

Бот получает данные от парсера и уведомляет
при появлении отключений на ваших улицах.`
	bot.Send(tgbotapi.NewMessage(chatID, text))
}

func handleAdd(bot *tgbotapi.BotAPI, chatID int64, street string) {
	street = strings.TrimSpace(street)
	if street == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "Укажите улицу. Пример: /add ул Ленина"))
		return
	}
	if err := addSubscription(chatID, street); err != nil {
		log.Printf("add subscription: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка."))
		return
	}
	bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ «%s» добавлена", street)))
}

func handleRemove(bot *tgbotapi.BotAPI, chatID int64, street string) {
	street = strings.TrimSpace(street)
	if street == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "Укажите улицу. Пример: /remove ул Ленина"))
		return
	}
	if err := removeSubscription(chatID, street); err != nil {
		log.Printf("remove subscription: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка."))
		return
	}
	bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 «%s» удалена", street)))
}

func handleList(bot *tgbotapi.BotAPI, chatID int64) {
	streets, err := getSubscriptions(chatID)
	if err != nil {
		log.Printf("get subscriptions: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка."))
		return
	}
	if len(streets) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 Нет отслеживаемых улиц.\nДобавьте: /add ул Название"))
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Отслеживаемые (%d):\n\n", len(streets)))
	for _, s := range streets {
		sb.WriteString(fmt.Sprintf("  • %s\n", s))
	}
	sendLong(bot, chatID, sb.String())
}

func handleCheckNow(bot *tgbotapi.BotAPI, chatID int64) {
	streets, err := getSubscriptions(chatID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка."))
		return
	}
	if len(streets) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "Сначала добавьте улицы через /add"))
		return
	}

	recordsCache.mu.RLock()
	records := make([]rosseti.ShutdownRecord, len(recordsCache.records))
	copy(records, recordsCache.records)
	recordsCache.mu.RUnlock()

	if len(records) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Нет данных от парсера. Парсер ещё не присылал информацию."))
		return
	}

	streetLower := make(map[string]bool)
	for _, s := range streets {
		streetLower[strings.ToLower(s)] = true
	}

	var matched int
	for _, o := range records {
		if !streetLower[strings.ToLower(o.Street)] {
			continue
		}
		sent, err := isSent(chatID, o.ID)
		if err != nil || sent {
			continue
		}
		matched++
		notifyOutage(bot, chatID, o)
		markSent(chatID, o.ID, o.Street)
	}

	if matched == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "✅ На ваших улицах новых отключений нет"))
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Отправлено уведомлений: %d", matched)))
	}
}

func main() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if err := initDB(); err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}
	log.Printf("Authorized as @%s", bot.Self.UserName)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/outages", handleOutagesWebhook(bot))
	mux.HandleFunc("GET /health", healthHandler)

	go func() {
		log.Printf("HTTP server on :%s", port)
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			log.Fatalf("http server: %v", err)
		}
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		chatID := update.Message.Chat.ID
		if update.Message.IsCommand() {
			args := strings.TrimSpace(update.Message.CommandArguments())
			switch update.Message.Command() {
			case "start":
				handleStart(bot, chatID)
			case "add":
				handleAdd(bot, chatID, args)
			case "remove":
				handleRemove(bot, chatID, args)
			case "list":
				handleList(bot, chatID)
			case "check":
				handleCheckNow(bot, chatID)
			}
		}
	}
}
