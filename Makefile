.PHONY: test watch-test maelctl maelstromd idl run-maelstromd cover
.EXPORT_ALL_VARIABLES:

GO111MODULE = on
MAEL_SQLDRIVER = sqlite3
MAEL_SQLDSN = ./tmp/maelstrom.db?cache=shared&_journal_mode=MEMORY
MAEL_PUBLICPORT = 8008

test:
	scripts/gofmt_check.sh
	rm -f pkg/v1/test.db pkg/gateway/test.db
	go test -timeout 4m ./...
	errcheck -ignore 'fmt:[FS]?[Pp]rint*' ./...

watch-test:
	find . -name *.go | entr -c make test

cover:
	mkdir -p tmp
	go test -coverprofile=tmp/cover.out github.com/coopernurse/maelstrom/pkg/gateway \
	    github.com/coopernurse/maelstrom/pkg/v1
	go tool cover -html=tmp/cover.out

maelctl:
	go build -o dist/maelctl --tags "libsqlite3 linux" cmd/maelctl/*.go

maelstromd:
	go build -o dist/maelstromd --tags "libsqlite3 linux" cmd/maelstromd/*.go

idl:
	barrister idl/maelstrom.idl | idl2go -i -p v1 -d pkg
	gofmt -w pkg/v1/*.go

run-maelstromd:
	mkdir -p tmp
	./dist/maelstromd &

profile-maelstromd:
	mkdir -p tmp
	./dist/maelstromd &

copy-to-server:
	scp ./dist/maelstromd root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/linux_x86_64/
	scp ./dist/maelctl root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/linux_x86_64/

copy-aws-scripts-to-server:
	scp ./cloud/aws/mael-init-node.sh root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/

gitbook:
	cd docs/gitbook && gitbook build

publish-web:
	make gitbook
	rm -rf docs/maelstromapp.com/docs/
	cp -r docs/gitbook/_book docs/maelstromapp.com/docs
	rsync -avz docs/maelstromapp.com/ root@maelstromapp.com:/opt/web/sites/maelstromapp.com/
