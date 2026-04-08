package middleware

import serv "github.com/tomasweigenast/goserv"

func Logging() serv.Middleware {
	return func(ctx serv.Context, next serv.Next) {
		next(ctx)
	}
}
