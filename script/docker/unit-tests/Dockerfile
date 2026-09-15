# Unit-test image for running Flynn's Linux unit suite from macOS/Windows
# via Docker Desktop. Keep deps aligned with .github/workflows/unit-tests.yml.
FROM ubuntu:24.04

ARG GO_VERSION=1.24.12
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
    build-essential \
    pkg-config \
    libseccomp-dev \
    libdevmapper-dev \
    postgresql \
    postgresql-contrib \
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
