.PHONY: test watch-test maelctl maelstromd idl run-maelstromd cover
.EXPORT_ALL_VARIABLES:

GO111MODULE = on

test:
	scripts/gofmt_check.sh
	go test ./...
	errcheck -ignore 'fmt:[FS]?[Pp]rint*' ./...

watch-test:
	find . -name *.go | entr -c make test

cover:
	mkdir -p tmp
	go test -coverprofile=tmp/cover.out gitlab.com/coopernurse/maelstrom/pkg/gateway \
	    gitlab.com/coopernurse/maelstrom/pkg/v1
	go tool cover -html=tmp/cover.out

maelctl:
	go build -o dist/maelctl cmd/maelctl/*.go

maelstromd:
	go build -o dist/maelstromd cmd/maelstromd/*.go

idl:
	barrister idl/maelstrom.idl | idl2go -i -p v1 -d pkg
	gofmt -w pkg/v1/*.go

run-maelstromd:
	mkdir -p tmp
	./dist/maelstromd -publicPort 8008 -sqlDriver sqlite3 \
	    -sqlDSN 'file:./tmp/maelstrom.db?cache=shared&_journal_mode=MEMORY' &
