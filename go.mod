module github.com/rowshape/rowshape

go 1.25.0

// 1.25.0, not 1.26.x: 1.25.0 is the lowest version the code and its dependencies
// actually build on (modelcontextprotocol/go-sdk requires >= 1.25.0), and the
// directive is a FLOOR on everyone who runs `go install github.com/rowshape/rowshape@latest`
// — one of the five advertised distribution channels. It previously read 1.26.5,
// a patch-level floor nothing needed, which locked out anyone even one patch
// release behind and stopped the pinned golangci-lint from reading this file at
// all. The patch component is forced by the dependency, not chosen: a bare
// `go 1.25` sorts BELOW `1.25.0` in the module graph and does not satisfy it.

require (
	github.com/jackc/pgx/v5 v5.7.6
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/spf13/cobra v1.10.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)
