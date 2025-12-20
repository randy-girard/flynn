FROM golang:1.19

WORKDIR /app

COPY . /app

RUN rm -f go.mod && \
  go mod init github.com/randy-girard/flynn-discovery && \
  go mod tidy && \
  go build -o /usr/bin/flynn-discovery

CMD ["/usr/bin/flynn-discovery"]
