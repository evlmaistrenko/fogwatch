package i18n

import (
	"fmt"

	"fogwatch/internal/telegram"
)

var texts = map[string]map[string]string{
	"ru": {
		"help": "Присылаю промокоды Dead by Daylight, как только они появляются.\n\n" +
			"Каждый код — отдельное сообщение: 📋 копирует, а активированный код просто удалите из чата.\n\n" +
			"/codes — активные коды\n/lang — язык",
		"admin_help": "\n\n<b>Админ</b>\nНайденные поиском коды сначала приходят только вам: ✅ публикует и рассылает, ❌ отклоняет.\n/pending — коды на модерации\n/all — все коды в базе (⏳ модерация, ⌛ истёк)\n/add КОД [ГГГГ-ММ-ДД] [награда] — добавить и сразу разослать (или просто пришлите код)\n/del КОД — удалить\n/fetch — искать сейчас",
		"version":    "\n\n<i>Версия %s</i>",
		"all":        "📚 <b>Все коды</b>",
		"no_codes":   "Активных кодов сейчас нет. Пришлю, как появятся.",
		"until":      "до %s",
		"copy":       "Скопировать",
		"pick_lang":  "Язык / Language",
		"lang_set":   "Язык: русский",

		"lang_auto_btn": "Как в Telegram",
		"lang_auto_set": "Язык будет как в Telegram (сейчас русский).",
		"no_access":     "Только для админа.",
		"added":         "Добавлен: <code>%s</code>",
		"updated":       "Обновлён: <code>%s</code>",
		"add_usage":     "Формат: /add КОД [ГГГГ-ММ-ДД] [награда]",
		"del_usage":     "Формат: /del КОД",
		"deleted":       "Удалён: <code>%s</code>",
		"not_found":     "Не найден.",
		"fetching":      "Ищу коды…",
		"fetch_done":    "Готово. Найдено: %d, новых на модерацию: %d.",
		"fetch_err":     "Поиск не удался. Подробности в логах.",
		"fetch_busy":    "Поиск уже идёт.",
		"db_err":        "Ошибка базы данных, попробуйте позже.",

		"mod_title":        "🔍 <b>Новый код — на модерации</b>",
		"source":           "Источник",
		"btn_approve":      "✅ Опубликовать",
		"btn_reject":       "❌ Отклонить",
		"approved":         "✅ Опубликован, рассылаю",
		"rejected":         "❌ Отклонён",
		"mod_done_already": "Уже обработан",
		"mod_published":    "✅ Опубликован",
		"mod_removed":      "❌ Отклонён или удалён",
		"no_pending":       "Нет кодов на модерации.",

		"fetch_alert":     "⚠️ Поиск кодов не работает: %d неудачных попыток подряд. Подробности в логах.",
		"fetch_recovered": "✅ Поиск кодов снова работает.",
	},
	"en": {
		"help": "I send Dead by Daylight promo codes as soon as they appear.\n\n" +
			"Each code is a separate message: 📋 copies it; once redeemed, just delete the message.\n\n" +
			"/codes — active codes\n/lang — language",
		"admin_help": "\n\n<b>Admin</b>\nCodes found by search come to you first: ✅ publishes and broadcasts, ❌ rejects.\n/pending — codes awaiting moderation\n/all — all codes in the database (⏳ pending, ⌛ expired)\n/add CODE [YYYY-MM-DD] [reward] — add and broadcast now (or just send the code)\n/del CODE — remove\n/fetch — search now",
		"version":    "\n\n<i>Version %s</i>",
		"all":        "📚 <b>All codes</b>",
		"no_codes":   "No active codes right now. I'll send them when they appear.",
		"until":      "until %s",
		"copy":       "Copy",
		"pick_lang":  "Язык / Language",
		"lang_set":   "Language: English",

		"lang_auto_btn": "Same as Telegram",
		"lang_auto_set": "Language will follow Telegram (currently English).",
		"no_access":     "Admins only.",
		"added":         "Added: <code>%s</code>",
		"updated":       "Updated: <code>%s</code>",
		"add_usage":     "Usage: /add CODE [YYYY-MM-DD] [reward]",
		"del_usage":     "Usage: /del CODE",
		"deleted":       "Removed: <code>%s</code>",
		"not_found":     "Not found.",
		"fetching":      "Searching…",
		"fetch_done":    "Done. Found: %d, new for moderation: %d.",
		"fetch_err":     "Search failed. Details are in the logs.",
		"fetch_busy":    "A search is already running.",
		"db_err":        "Database error, please try again later.",

		"mod_title":        "🔍 <b>New code — awaiting moderation</b>",
		"source":           "Source",
		"btn_approve":      "✅ Publish",
		"btn_reject":       "❌ Reject",
		"approved":         "✅ Published, broadcasting",
		"rejected":         "❌ Rejected",
		"mod_done_already": "Already handled",
		"mod_published":    "✅ Published",
		"mod_removed":      "❌ Rejected or deleted",
		"no_pending":       "Nothing awaiting moderation.",

		"fetch_alert":     "⚠️ Code search is failing: %d attempts in a row. Details are in the logs.",
		"fetch_recovered": "✅ Code search works again.",
	},
}

func T(lang, key string, args ...any) string {
	m, ok := texts[lang]
	if !ok {
		m = texts["en"]
	}
	s := m[key]
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Detect maps a Telegram language_code to a supported language;
// "" when Telegram didn't send one (keep what we have).
func Detect(code string) string {
	switch {
	case code == "":
		return ""
	case code == "ru", code == "uk", code == "be", code == "kk":
		return "ru"
	}
	return "en"
}

var Commands = map[string][]telegram.Cmd{
	"ru": {
		{Command: "codes", Description: "Активные коды"},
		{Command: "lang", Description: "Язык"},
		{Command: "help", Description: "Помощь"},
	},
	"en": {
		{Command: "codes", Description: "Active codes"},
		{Command: "lang", Description: "Language"},
		{Command: "help", Description: "Help"},
	},
}

// AdminCommands are appended to the menu shown in admins' chats.
var AdminCommands = map[string][]telegram.Cmd{
	"ru": {
		{Command: "pending", Description: "На модерации"},
		{Command: "all", Description: "Все коды"},
		{Command: "add", Description: "Добавить код"},
		{Command: "del", Description: "Удалить код"},
		{Command: "fetch", Description: "Искать сейчас"},
	},
	"en": {
		{Command: "pending", Description: "Awaiting moderation"},
		{Command: "all", Description: "All codes"},
		{Command: "add", Description: "Add code"},
		{Command: "del", Description: "Remove code"},
		{Command: "fetch", Description: "Search now"},
	},
}
