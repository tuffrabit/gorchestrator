package server

import "encoding/json"

func jsonMarshalImpl(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshalImpl(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
