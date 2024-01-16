module github.com/veertuinc/anklet

go 1.22.2

require (
	github.com/gofri/go-github-ratelimit v1.1.0
	github.com/google/go-github/v58 v58.0.0
	github.com/google/uuid v1.6.0
	github.com/norsegaud/go-daemon v0.1.10
	github.com/redis/go-redis/v9 v9.5.1
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/kardianos/osext v0.0.0-20190222173326-2bc1f35cddc0 // indirect
	golang.org/x/sys v0.19.0 // indirect
)

replace github.com/veertuinc/anklet/plugins/github => ./plugins/github
