# rtfm devbox: a blank-slate developer machine.
#
# A typical dev toolchain (git, build tools, Python, Node, Go) and nothing
# project-specific. Deliberately NOT preinstalled: any AI CLIs (agy, gemini…),
# cloud SDKs, or language package caches, so quickstarts must say how to get
# what they need.
FROM ubuntu:24.04

ARG NODE_VERSION=22.12.0
ARG GO_VERSION=1.27.3

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget git openssh-client gnupg sudo \
        build-essential pkg-config make unzip zip xz-utils jq less file \
        python3 python3-venv python3-pip python3-dev pipx \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
        | tar -xJ -C /usr/local --strip-components=1 \
    && node --version && npm --version

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
        | tar -xz -C /usr/local \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt \
    && go version

# Common "python" alias that many READMEs assume.
RUN ln -s /usr/bin/python3 /usr/local/bin/python

# A default, already-active Python environment, like a typical developer
# machine (pyenv, conda, Homebrew, devcontainers): `pip install x` works
# instead of tripping Ubuntu's PEP 668 "externally managed" guard, which is
# a property of this image, not of the docs under test. Projects that make
# their own venv (`python3 -m venv .venv`) are unaffected.
RUN python3 -m venv /opt/python && /opt/python/bin/pip install --no-cache-dir --upgrade pip

ENV VIRTUAL_ENV=/opt/python \
    PATH=/opt/python/bin:/root/.local/bin:/root/go/bin:$PATH
WORKDIR /root
CMD ["sleep", "infinity"]
