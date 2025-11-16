FROM golang:1.19

WORKDIR /app

COPY . /app

RUN go mod init && \
  go build

CMD ["/app/flynn-discovery"]
