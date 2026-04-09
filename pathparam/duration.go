package pathparam

import "time"

type DurationDecoder struct{}

func (DurationDecoder) Zero() any                      { return time.Duration(0) }
func (DurationDecoder) Decode(raw string) (any, error) { return time.ParseDuration(raw) }
