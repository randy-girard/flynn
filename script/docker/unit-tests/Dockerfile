# Unit-test image for running Flynn's Linux unit suite from macOS/Windows
# via Docker Desktop. Keep deps aligned with .github/workflows/unit-tests.yml.
FROM ubuntu:24.04

ARG GO_VERSION=1.24.12
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    wget \
    build-essential \
    pkg-config \
    libseccomp-dev \
    libdevmapper-dev \
    postgresql \
    postgresql-contrib \
    mariadb-server \
    mariadb-backup \
    redis-server \
    zfsutils-linux \
    bats \
    jq \
    git \
    sudo \
    file \
    locales \
    && locale-gen en_US.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8
ENV LANGUAGE=en_US:en

# MongoDB appliance unit tests need mongod (not in Ubuntu main).
RUN curl -fsSL https://www.mongodb.org/static/pgp/server-8.0.asc \
      | gpg -o /usr/share/keyrings/mongodb-server-8.0.gpg --dearmor \
    && echo "deb [ arch=amd64,arm64 signed-by=/usr/share/keyrings/mongodb-server-8.0.gpg ] https://repo.mongodb.org/apt/ubuntu noble/mongodb-org/8.0 multiverse" \
      > /etc/apt/sources.list.d/mongodb-org-8.0.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends mongodb-org \
    && rm -rf /var/lib/apt/lists/*

RUN arch="$(dpkg --print-architecture)" \
    && wget -q "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" \
    && rm -rf /usr/local/go \
    && tar -C /usr/local -xzf "go${GO_VERSION}.linux-${arch}.tar.gz" \
    && rm "go${GO_VERSION}.linux-${arch}.tar.gz"

ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV GOFLAGS=-mod=vendor
ENV GOPATH=/go

# Allow passwordless sudo for postgres peer wrappers used by appliance tests.
RUN echo 'root ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/root-nopasswd \
    && chmod 0440 /etc/sudoers.d/root-nopasswd \
    && echo 'postgres ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/postgres-nopasswd \
    && chmod 0440 /etc/sudoers.d/postgres-nopasswd

WORKDIR /src

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
