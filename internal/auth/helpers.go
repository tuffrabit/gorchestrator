package auth

import (
	"encoding/json"
	"os"
)

func getenv(k string) string {
	return os.Getenv(k)
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
