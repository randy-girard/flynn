FROM golang:1.24-alpine

WORKDIR /app

RUN apk add --no-cache git bash

COPY . /app

RUN go mod init github.com/flynn/go-tuf && \
  go get github.com/docker/docker@v20.10.24+incompatible && \
  go mod tidy && \
  go install ./cmd/tuf

CMD ["/app/init.sh"]
