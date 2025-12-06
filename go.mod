module github.com/veertuinc/anklet

go 1.24.1

require (
	github.com/bradleyfalzon/ghinstallation/v2 v2.17.0
	github.com/gofri/go-github-ratelimit/v2 v2.0.2
	github.com/google/go-github/v74 v74.0.0
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.17.2
	github.com/shirou/gopsutil/v4 v4.25.11
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/ebitengine/purego v0.9.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/google/go-github/v75 v75.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20251013123823-9fd1530e3ec3 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sys v0.38.0 // indirect
)

replace github.com/veertuinc/anklet/plugins/handlers/github => ./plugins/handlers/github

replace github.com/veertuinc/anklet/plugins/receivers/github => ./plugins/receivers/github
