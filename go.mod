module github.com/veertuinc/anklet

go 1.24.1

require (
	github.com/bradleyfalzon/ghinstallation/v2 v2.11.0
	github.com/gofri/go-github-ratelimit v1.1.0
	github.com/google/go-github/v66 v66.0.0
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.5.5
	github.com/shirou/gopsutil/v4 v4.24.10
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/ebitengine/purego v0.8.1 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.1 // indirect
	github.com/google/go-github/v62 v62.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sys v0.26.0 // indirect
)

replace github.com/veertuinc/anklet/plugins/handlers/github => ./plugins/handlers/github

replace github.com/veertuinc/anklet/plugins/receivers/github => ./plugins/receivers/github
