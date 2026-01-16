ARG HOST_UBUNTU
FROM ubuntu:${HOST_UBUNTU:-16.04}

# Install dependencies
RUN apt-get update && apt-get install -y \
    wget \
    tar \
    ca-certificates \
    git \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Download and install Go 1.21.13
RUN wget https://go.dev/dl/go1.21.13.linux-amd64.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf go1.21.13.linux-amd64.tar.gz && \
    rm go1.21.13.linux-amd64.tar.gz

# Set Go environment
ENV PATH="/usr/local/go/bin:${PATH}"

# Optional: check Go version and CGO
RUN go version && \
    go env CGO_ENABLED && \
    CGO_ENABLED=1 go env CGO_ENABLED

WORKDIR /app

COPY . /app

RUN rm -f go.mod && \
  go mod init github.com/randy-girard/flynn-discovery && \
  go mod tidy && \
  go build -o /usr/bin/flynn-discovery

CMD ["/bin/flynn-discovery"]
