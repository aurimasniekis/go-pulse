package natsauth

import "encoding/json"

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
