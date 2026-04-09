package pathparam

type ParamDecoder interface {
	Zero() any
	Decode(raw string) (any, error)
}

func NewParamDecoder[T any](fn func(string) (T, error)) ParamDecoder {
	return &typedDecoder[T]{fn: fn}
}

type typedDecoder[T any] struct{ fn func(string) (T, error) }

func (*typedDecoder[T]) Zero() any                        { var zero T; return zero }
func (d *typedDecoder[T]) Decode(raw string) (any, error) { return d.fn(raw) }
