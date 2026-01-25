FROM golang:1.24-alpine

WORKDIR /app

RUN apk add --no-cache git bash

COPY . /app

# Remove tuf-client to avoid missing client package dependency
RUN rm -rf ./cmd/tuf-client && \
  go mod init github.com/flynn/go-tuf && \
  go get github.com/docker/docker@v20.10.24+incompatible && \
  go mod edit -replace github.com/flynn/go-tuf=./ && \
  go mod tidy && \
  go install ./cmd/tuf

CMD ["/app/init.sh"]
