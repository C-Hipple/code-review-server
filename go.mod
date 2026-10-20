module crs

go 1.26.0

// The extension's npm dependencies ship Go sources (flatted) that are not part
// of this module.
ignore ./chrome_extension/node_modules

require (
	github.com/google/go-github/v74 v74.0.0
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/oauth2 v0.37.0
)

require (
	github.com/google/go-github/v92 v92.0.0
	github.com/google/go-querystring v1.2.0 // indirect
)
