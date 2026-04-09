package pathparam

import "time"

type TimeDecoder struct{}

func (TimeDecoder) Zero() any { return time.Time{} }
func (TimeDecoder) Decode(raw string) (any, error) {
	return time.Parse(time.RFC3339, raw)
}
