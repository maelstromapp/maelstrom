.PHONY: test maelctl maelstromd

test:
	scripts/gofmt_check.sh

maelctl:
	go build -o dist/maelctl cmd/maelctl/*.go

maelstromd:
	go build -o dist/maelstromd cmd/maelstromd/*.go

