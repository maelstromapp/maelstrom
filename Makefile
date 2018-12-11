.PHONY: test watch-test maelctl maelstromd idl
.EXPORT_ALL_VARIABLES:

GO111MODULE = on

test:
	scripts/gofmt_check.sh
	errcheck -ignore 'fmt:[FS]?[Pp]rint*' ./...
	go test -v ./...

watch-test:
	find . -name *.go | entr -c make test

maelctl:
	go build -o dist/maelctl cmd/maelctl/*.go

maelstromd:
	go build -o dist/maelstromd cmd/maelstromd/*.go

idl:
	mkdir -p pkg/maelstrom/v1
	barrister idl/maelstrom.idl | idl2go -i -p v1 -d pkg
	gofmt -w pkg/v1/*.go
