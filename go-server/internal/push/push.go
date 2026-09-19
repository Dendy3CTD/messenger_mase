package push

import (
	"log"

	"github.com/mase/server/internal/db"
)

// SendNotification sends a push notification to a user.
// Phase 0: logs only. Phase 4 will integrate FCM.
func SendNotification(toUserID int64, senderName, body string, chatID int64) {
	token, err := db.GetPushToken(toUserID)
	if err != nil || token == "" {
		return
	}
	log.Printf("[PUSH] stub → uid=%d chat=%d from=%q body=%q token=%s…",
		toUserID, chatID, senderName, truncate(body, 40), token[:8])
	// TODO Phase 4: call FCM API
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}
