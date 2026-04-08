package goserv

type Next func(Context)

type Middleware func(ctx Context, next Next)
