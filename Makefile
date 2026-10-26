# The deploy target needs the SSH target, e.g.  make deploy SERVER=root@your-host
SERVER   ?=
IMAGE    ?= c3pkg:latest

.PHONY: run test vet browser-test deploy logs

run:
	go run ./cmd/c3pkg

vet:
	go vet ./...

test: vet
	go test ./...

# Needs Chromium and a built binary at /tmp/c3pkg-bin; see web/test/e2e.test.mjs.
browser-test:
	go build -o /tmp/c3pkg-bin ./cmd/c3pkg
	cd web/test && npm install --silent && npm test

# Ship the source to the server, build the image there and restart the container.
# First-time setup (data dir, .env, Caddy block) is described in DEPLOY.md.
deploy: test
	@test -n "$(SERVER)" || { echo "set SERVER, e.g. make deploy SERVER=root@your-host"; exit 1; }
	tar --exclude=.git --exclude=data --exclude=.env --exclude=node_modules --exclude=web/test/shots -czf - . \
	  | ssh $(SERVER) 'rm -rf /srv/c3pkg/src && mkdir -p /srv/c3pkg/src && tar -xzf - -C /srv/c3pkg/src'
	ssh $(SERVER) 'cd /srv/c3pkg/src && docker build -t $(IMAGE) . && sh deploy/run.sh'

logs:
	@test -n "$(SERVER)" || { echo "set SERVER, e.g. make logs SERVER=root@your-host"; exit 1; }
	ssh $(SERVER) 'docker logs --tail 100 -f c3pkg'
