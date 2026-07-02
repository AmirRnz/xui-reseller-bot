package i18n

import (
	"fmt"
)

func T(lang, key string) string {
	// Fallback mechanism to be implemented, default to english for now
	return fmt.Sprintf("[%s:%s]", lang, key)
}
