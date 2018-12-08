.PHONY: test maelctl maelstromd idl

test:
	scripts/gofmt_check.sh

maelctl:
	go build -o dist/maelctl cmd/maelctl/*.go

maelstromd:
	go build -o dist/maelstromd cmd/maelstromd/*.go

idl:
	mkdir -p pkg/maelstrom/v1
	barrister idl/maelstrom.idl | idl2go -i -p v1 -d pkg/maelstrom
	gofmt -w pkg/maelstrom/v1/*.go
