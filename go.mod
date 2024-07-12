module github.com/veertuinc/anklet

go 1.22.2

require (
	github.com/bradleyfalzon/ghinstallation/v2 v2.10.0
	github.com/gofri/go-github-ratelimit v1.1.0
	github.com/google/go-github/v61 v61.0.0
	github.com/google/uuid v1.6.0
	github.com/norsegaud/go-daemon v0.1.10
	github.com/redis/go-redis/v9 v9.5.1
	github.com/shirou/gopsutil/v3 v3.24.4
	github.com/veertuinc/webhooks/v6 v6.0.1
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.0 // indirect
	github.com/google/go-github/v60 v60.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/kardianos/osext v0.0.0-20190222173326-2bc1f35cddc0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/shoenig/go-m1cpu v0.1.6 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sys v0.19.0 // indirect
)

replace github.com/veertuinc/anklet/plugins/github => ./plugins/github

replace github.com/veertuinc/anklet/plugins/controllers => ./plugins/controllers
