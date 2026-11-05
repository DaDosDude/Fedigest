module fedigest

go 1.24.7

require (
	github.com/mattn/go-sqlite3 v1.14.52
	golang.org/x/crypto v0.31.0
)

require github.com/yuin/goldmark v1.8.6

replace golang.org/x/crypto => github.com/golang/crypto v0.31.0
