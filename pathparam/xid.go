package pathparam

import "github.com/rs/xid"

type XIDDecoder struct{}

func (XIDDecoder) Zero() any                      { return xid.ID{} }
func (XIDDecoder) Decode(raw string) (any, error) { return xid.FromString(raw) }
